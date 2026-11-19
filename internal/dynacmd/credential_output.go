package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// runosSecretPattern matches every currently supported RunOS/Roid bearer
// secret shape. Ported from Roid's own authoritative definition
// (roid/internal/secretshape/secretshape.go, commit da5fdcbd97c4, the
// head of Roid story 365) rather than re-derived, so this gate and the
// producer's own redaction never drift apart:
//
//   - typed:  runos_(pat|key)_<canonical-lowercase-uuid>.<43-char unpadded base64url>
//     (a Personal Access Token or an Account API Key minted through the
//     new typed Roid routes; FPL411's bearer format, contract 1.0.1).
//   - legacy: runos_pat_<10-char unambiguous>.<32-char unambiguous>
//     (Conductor's existing /:aid/account/api-keys routes, apiKeys.ts;
//     same unambiguous charset as KEY_ID_LENGTH/SECRET_LENGTH there).
//   - notify: runos_ntk_<10-char unambiguous>.<32-char unambiguous>, 53
//     characters total (handbook article 220, create-notify-key: "The
//     keyValue is a token of the form runos_ntk_<id>.<secret>, 53
//     characters long"; the 10/32 split matches that total exactly and
//     mirrors apiKeys.ts's own generator, which account/notify-keys
//     shares). Review finding 1150: a notify-key leak into an inventory
//     response went uncaught because this pattern had no notify shape.
//   - roid:   rk_<43-char unpadded base64url> (Roid's own legacy
//     personal-key secret, still minted by
//     roid/internal/server/personalkeys.go and accounts.go).
//
// Roid's secretshape.go also recognises a family of short-prefixed
// internal bearer shapes (at_, rt_, mcp_, oat_, xat_, prq_, prd_, prr_,
// rrt_), Roid's own session/exchange/approval tokens that a CLI user
// never presents as a RunOS credential. Deliberately NOT ported here:
// measured against this repo's own tree, those 2-4 character prefixes
// false-positive inside ordinary long Go identifiers (a test named
// ...Report_Local... contains "rt_"; ...Format_JSON... contains "at_"),
// each immediately followed by 43+ further identifier characters that
// happen to satisfy the base64url class. rk_ is kept because it is the
// one roid-internal shape finding 1106 confirmed still reaches a RunOS
// credential a person could hold, and it carries lower collision risk
// than the two-letter prefixes.
//
// No trailing word-boundary assertion: a base64url secret can end in `-`
// or `_`, which is not a `\b` transition in Go's RE2 engine, and missing
// the true end of a secret there would be a false NEGATIVE. Over-matching
// a few extra characters only makes this gate stricter, which is the safe
// direction for a leak-prevention check.
var runosSecretPattern = regexp.MustCompile(
	`runos_(?:pat|key)_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[A-Za-z0-9_-]{43}` +
		`|runos_pat_[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{10}\.[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{32}` +
		`|runos_ntk_[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{10}\.[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{32}` +
		`|rk_[A-Za-z0-9_-]{43}`,
)

// Review finding 1193 (third occurrence of 1105/1150's root defect): the
// one-shot exemption used to accept ANY of runosSecretPattern's
// alternatives for ANY family's issuing field, so a family that only
// ever mints ONE specific bearer shape would silently exempt a
// DIFFERENT shape too -- e.g. the legacy account/api-keys/add route
// returning a typed runos_key_ bearer (a server-side regression
// substituting the wrong credential family) sailed through unexamined
// because it still matched the shared union pattern. Each family's own
// producer mints exactly one shape; the exemption must check for THAT
// shape only; a secret of any other recognised shape is RENDERED
// UNSAFE -- i.e. refused -- the same as any value with no license at
// all would be.
var (
	legacyAPIKeyTokenPattern = regexp.MustCompile(
		`^runos_pat_[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{10}\.[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{32}$`,
	)
	legacyNotifyKeyPattern = regexp.MustCompile(
		`^runos_ntk_[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{10}\.[ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789]{32}$`,
	)
	typedPersonalAccessTokenSecretPattern = regexp.MustCompile(
		`^runos_pat_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[A-Za-z0-9_-]{43}$`,
	)
	typedAccountAPIKeySecretPattern = regexp.MustCompile(
		`^runos_key_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.[A-Za-z0-9_-]{43}$`,
	)
)

// credentialFamily names the one JSON field a credential command
// family's issuing operations may carry a bearer secret under, which
// verbs are issuing operations, and the EXACT bearer shape (anchored,
// whole-string) that family's own producer mints for that field. Every
// other verb in the family is pure inventory: no secret-shaped value is
// permitted anywhere in its response.
type credentialFamily struct {
	secretField   string
	secretPattern *regexp.Regexp
	issuanceVerbs map[string]bool
}

// credentialCommandFamilies is the exhaustive, central list of command
// families this guard scopes itself to. A command whose path does not
// start with one of these prefixes is not scanned at all: review finding
// 1097 measured that an earlier, unscoped version of this guard refused
// unrelated commands (app logs, env reads, exec-sql results) whenever
// their output happened to contain a credential-shaped string, which
// turns a defensive check into a denial of service on the user's own
// data. Scoping to named families, rather than scattering command
// literals through the executor, keeps that list in one place.
//
// Verbs are the PUBLISHED manifest command spellings, verified directly
// against Conductor's source (review finding 1155): "account/api-keys"
// mints TWO issuing commands, `add` (admin, account-role key) and
// `add-personal` (self-serve personal key) -- src/util/cliManifest/
// apiKeys.manifest.ts, commit c469a241 -- both returning the bearer
// under `token`. An earlier revision wrote the verb as "personal", which
// matches no published command, so `add-personal`'s own token was
// refused as a leak (its only copy, then unrecoverable). "account/
// notify-keys" mints one issuing command, `add`, under `keyValue" --
// src/util/cliManifest/account.manifest.ts.
//
// "account/personal-access-tokens" and "account/account-api-keys" are
// published for preview/list/show only; their add/update/rotate/revoke
// commands are deliberately held back by Conductor itself
// (HEADER_TRANSPORT_SUPPORTED = false, src/util/cliManifest/
// credentialHeaderFields.ts, commit 4a7af046) until the CLI can route
// `idempotencyKey`/`ifMatch` as real HTTP headers (review finding 1151;
// see internal/manifest's Field.Location and executor_request.go's
// header-field transport). Their verbs and field name ("secret") are
// taken from Roid's actual shipped response shape
// (writePATWithSecret/writeAccountKeyWithSecret,
// roid/internal/server/personalaccesstokens.go,accountapikeys.go,
// commit da5fdcbd97c4), which Conductor's relay forwards unchanged
// (story 382, FPL520) and which Conductor's own manifest source
// (personalAccessTokens.manifest.ts, accountApiKeys.manifest.ts,
// commit 4a7af046) confirms verbatim. Re-check against the manifest
// again once Conductor flips the gate and these commands go live.
var credentialCommandFamilies = map[string]credentialFamily{
	"account/api-keys": {
		secretField:   "token",
		secretPattern: legacyAPIKeyTokenPattern,
		issuanceVerbs: map[string]bool{"add": true, "add-personal": true},
	},
	"account/notify-keys": {
		secretField:   "keyValue",
		secretPattern: legacyNotifyKeyPattern,
		issuanceVerbs: map[string]bool{"add": true},
	},
	"account/personal-access-tokens": {
		secretField:   "secret",
		secretPattern: typedPersonalAccessTokenSecretPattern,
		issuanceVerbs: map[string]bool{"add": true, "rotate": true},
	},
	"account/account-api-keys": {
		secretField:   "secret",
		secretPattern: typedAccountAPIKeySecretPattern,
		issuanceVerbs: map[string]bool{"add": true, "rotate": true},
	},
}

// credentialFamilyFor reports whether command belongs to a registered
// credential family, and its verb (everything after "<family>/").
//
// Matches by registered PREFIX rather than "everything before the last
// slash" (review finding 1155's second instance): a command spelled
// with an embedded path segment after the family (for example a
// hypothetical "account/notify-keys/{id}/delete") would otherwise split
// into a family key ("account/notify-keys/{id}") that is not registered,
// dropping the WHOLE family out of the guard unscanned, rather than
// just failing to recognise one unusual verb. Prefix matching keeps the
// family recognised (and therefore still scanned in full) regardless of
// how many segments follow it; only the one-shot exemption needs an
// exact verb match.
func credentialFamilyFor(command string) (family credentialFamily, verb string, ok bool) {
	for prefix, f := range credentialCommandFamilies {
		if rest, found := strings.CutPrefix(command, prefix+"/"); found {
			return f, rest, true
		}
	}
	return credentialFamily{}, "", false
}

// guardCredentialResponse refuses to let a secret-shaped bearer value
// reach the terminal or a --json consumer from a credential command
// family's response, except through the exact one-shot field its own
// issuing operation documents.
//
// Scoped to credentialCommandFamilies ONLY (see its doc for why): a
// command outside that registry returns nil immediately and is never
// scanned, so this guard has no effect on any other command's output
// (finding 1097). Within a registered family, an inventory verb (list,
// show, update, revoke, ...) permits no secret-shaped value anywhere; an
// issuing verb (add, rotate) permits it ONLY as the family's own named
// top-level field, and ONLY when that field's value is nothing but the
// one bearer (isExactSecretMatch) -- not merely contains one (finding
// 1150: a value with two concatenated bearers rendered both under the
// old substring check). Every other occurrence is refused, including a
// second copy under a different key, one nested inside an object or
// array, or one used as a PROPERTY NAME rather than a value.
//
// A malformed or non-JSON body is scanned as raw text instead of being
// passed through unchecked (finding 1150: the previous version returned
// nil on any unmarshal failure, and internal/output/formatter.go prints
// an unparsed body verbatim in text mode).
//
// Decodes with a STREAMING json.Decoder reading exactly one value,
// never json.Unmarshal, to match internal/output's JSON formatter byte
// for byte (formatter.go's pretty-print path: a Decoder that decodes
// the first value and silently ignores anything after it). Finding
// 1193: a body carrying one valid JSON value followed by trailing
// non-whitespace data made json.Unmarshal fail here (it requires the
// WHOLE input to be one value), diverting this guard onto the raw-byte
// fallback -- a different, weaker check than the structured per-field
// scan the decoded value would have gotten -- while the formatter
// rendered the decoded first value exactly as if it were the whole
// response. Using the same decode semantics here means this guard
// always examines precisely what the formatter is about to render, and
// a credential-family response with ANY trailing data after its one
// JSON value is refused outright: Roid/Conductor never emit that shape,
// so the safe assumption is that something is wrong with it.
func guardCredentialResponse(cmdDef manifest.Command, respBody []byte) error {
	family, verb, ok := credentialFamilyFor(cmdDef.Command)
	if !ok {
		return nil
	}
	trimmed := bytes.TrimSpace(respBody)
	if len(trimmed) == 0 {
		return nil
	}
	var probe any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	// Finding 1215 (G1, 4th occurrence of 1105/1150/1193's root defect):
	// internal/output/formatter.go's JSON renderer calls UseNumber before
	// decoding; this guard did not, so a body carrying a number outside
	// float64's exact range (decoded into `any` without UseNumber,
	// strconv.ParseFloat returns ErrRange) made THIS decode fail while
	// the formatter's decode of the SAME bytes succeeded, diverting this
	// guard onto the weaker raw-byte fallback for a body the formatter
	// renders structurally. UseNumber here matches the formatter's mode
	// exactly, so this guard fails to decode only when the formatter
	// would also fail to decode.
	decoder.UseNumber()
	if err := decoder.Decode(&probe); err != nil {
		if runosSecretPattern.Match(trimmed) {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in a non-JSON or malformed response body, which the inventory contract forbids")
		}
		return nil
	}
	var trailing json.RawMessage
	if derr := decoder.Decode(&trailing); derr != io.EOF {
		return fmt.Errorf("refusing to render response: the response body carries unexpected trailing data after its JSON value, which the inventory contract forbids for a credential command")
	}
	obj, isObject := probe.(map[string]any)
	if !isObject {
		if found := findSecretValue(probe, "response"); found != "" {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in %s, which the inventory contract forbids", found)
		}
		return nil
	}
	allowedField := ""
	if family.issuanceVerbs[verb] {
		allowedField = family.secretField
	}
	for key, value := range obj {
		if runosSecretPattern.MatchString(key) {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in a property name, which the inventory contract forbids")
		}
		if allowedField != "" && key == allowedField {
			// Finding 1215 (G3): a value in the issuing field that matches
			// NEITHER this family's own shape NOR any OTHER recognised
			// shape used to fall through to findSecretValue below, which
			// found nothing to object to and let it render -- the banners
			// print any non-empty string, so a malformed or garbage
			// "token" was presented to the user as a valid one-shot
			// secret. The issuing field must hold EXACTLY the shape its
			// own producer mints, or the response is refused outright.
			s, isString := value.(string)
			if isString && family.secretPattern != nil && family.secretPattern.MatchString(s) {
				continue // the one legitimate carrier, on the one operation documented to return it, holding exactly the shape that operation's own producer mints
			}
			return fmt.Errorf("refusing to render response: the issuing field %q did not hold the exact bearer shape this operation's own producer mints, which the inventory contract forbids", key)
		}
		if found := findSecretValue(value, key); found != "" {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in field %q, which the inventory contract forbids", found)
		}
	}
	// Finding 1246 (repeat of 1215's G1, 5th occurrence of 1105/1150/
	// 1193's root defect): the scan above examines the DECODED structure
	// only. Two things can carry a secret in the RAW bytes that the
	// decoded structure does not account for: a JSON object with a
	// duplicate key, where Go's decoder keeps only the LAST value and the
	// scan above never sees an earlier, shadowed occurrence; and (for
	// internal/output/formatter.go's text-mode fallback specifically,
	// which prints `data` verbatim when a command declares no Output)
	// any raw text the decoded value's own re-rendering would not
	// reproduce. Comparing a raw match COUNT against the exactly one
	// occurrence the one-shot exemption accounts for (zero for every
	// other verb) catches both without re-litigating the exemption
	// itself: an authorized issuance's own single, legitimate secret
	// never exceeds its own count.
	allowed := 0
	if allowedField != "" {
		if s, ok := obj[allowedField].(string); ok && family.secretPattern != nil && family.secretPattern.MatchString(s) {
			allowed = 1
		}
	}
	if rawMatches := len(runosSecretPattern.FindAll(trimmed, -1)); rawMatches > allowed {
		return fmt.Errorf("refusing to render response: the raw response body carries more bearer-secret-shaped occurrences than its one authorized field accounts for, which the inventory contract forbids")
	}
	return nil
}

// guardCredentialErrorBody scans a credential-family command's error
// response (any status, any shape) for a secret-shaped value and, if
// found, replaces err with a generic refusal that carries none of the
// original body text. Finding 1150: the success-path guard ran only
// after dispatch returned without error, so a 401/422 (or any other)
// error body carrying a canary in an auxiliary field reached the normal
// error formatters (formatAuthError, the --json error envelope, the
// plain error return) unexamined. An error body never has a legitimate
// reason to carry a real secret, so there is no one-shot exemption here:
// any match anywhere (key or value, decoded or raw-fallback) refuses.
//
// Returns the ORIGINAL err unchanged when cmdDef is not a registered
// credential family, or when no secret-shaped value is found, so every
// other command's error formatting (dependents, module-not-enabled,
// auth hints) is unaffected.
func guardCredentialErrorBody(cmdDef manifest.Command, err error) error {
	apiErr, ok := err.(*APIError)
	if !ok {
		return err
	}
	if _, _, ok := credentialFamilyFor(cmdDef.Command); !ok {
		return err
	}
	if !responseContainsSecret(apiErr.Body) {
		return err
	}
	return fmt.Errorf("refusing to render error response (HTTP %d): a bearer-secret-shaped value was found, which the inventory contract forbids", apiErr.StatusCode)
}

// responseContainsSecret reports whether raw contains a secret-shaped
// value anywhere: decoded JSON keys and values when raw parses as JSON,
// the raw bytes directly as a fallback when it does not (a malformed or
// non-JSON error body must not get a pass just because it didn't parse).
//
// Finding 1215 (G1): decodes with UseNumber, matching
// describeAPIError's eventual rendering of the same bytes (and
// guardCredentialResponse's success-path decode) exactly, so a number
// outside float64's range elsewhere in the body cannot make this decode
// fail while a renderer downstream still succeeds on it.
func responseContainsSecret(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	var probe any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&probe); err != nil {
		return runosSecretPattern.Match(trimmed)
	}
	// Finding 1246: an error body never has a legitimate secret anywhere
	// (no one-shot exemption exists on this path, unlike
	// guardCredentialResponse), so a raw-byte match is an unconditional
	// leak regardless of whether the decoded structure also sees it.
	// describeAPIError prints the RAW body verbatim whenever the decoded
	// envelope's own "error" field is empty or missing; trailing data
	// after the one decoded value, or an earlier occurrence of a
	// duplicate key that Go's decoder discards in favour of the last
	// one, both reach that raw-print path unexamined by
	// valueContainsSecret(probe) alone.
	if runosSecretPattern.Match(trimmed) {
		return true
	}
	return valueContainsSecret(probe)
}

func valueContainsSecret(v any) bool {
	switch t := v.(type) {
	case string:
		return runosSecretPattern.MatchString(t)
	case map[string]any:
		for k, nested := range t {
			if runosSecretPattern.MatchString(k) || valueContainsSecret(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range t {
			if valueContainsSecret(nested) {
				return true
			}
		}
	}
	return false
}

// findSecretValue walks v looking for a string matching
// runosSecretPattern, in both property NAMES and values. path names the
// current field for the error message; a property name that itself
// matches is reported as "<redacted-key>" rather than echoing the
// secret-shaped name back into a message a person reads.
func findSecretValue(v any, path string) string {
	switch t := v.(type) {
	case string:
		if runosSecretPattern.MatchString(t) {
			return path
		}
	case map[string]any:
		for k, nested := range t {
			if runosSecretPattern.MatchString(k) {
				return path + ".<redacted-key>"
			}
			if found := findSecretValue(nested, path+"."+k); found != "" {
				return found
			}
		}
	case []any:
		for i, nested := range t {
			if found := findSecretValue(nested, fmt.Sprintf("%s[%d]", path, i)); found != "" {
				return found
			}
		}
	}
	return ""
}

// sanitizeForTableRender removes a credential family's one-shot secret
// field from a successful issuance response before it reaches the
// generic table/object formatter in TEXT mode, so the secret appears
// exactly once (in the dedicated banner printOneShotBanner prints),
// never twice (finding 1150: the generic formatter row and the banner
// each printed the same bearer). --json and every non-issuing response
// are returned unchanged: --json has no banner to duplicate against,
// and an inventory response never carries the field in the first place.
func sanitizeForTableRender(cmdDef manifest.Command, respBody []byte, jsonOutput bool) []byte {
	if jsonOutput {
		return respBody
	}
	family, verb, ok := credentialFamilyFor(cmdDef.Command)
	if !ok || !family.issuanceVerbs[verb] {
		return respBody
	}
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return respBody
	}
	if _, present := item[family.secretField]; !present {
		return respBody
	}
	delete(item, family.secretField)
	sanitized, err := json.Marshal(item)
	if err != nil {
		return respBody
	}
	return sanitized
}

// injectETagIntoCredentialResponse adds the response's ETag HEADER as a
// top-level "etag" field on a credential-family response, so a later
// update/rotate/revoke's If-Match (the manifest's Location:"header"
// ifMatch field) has something to present. Review finding 1151:
// Conductor's personalAccessTokens.manifest.ts and
// accountApiKeys.manifest.ts state explicitly that Roid computes
// If-Match as a digest of the response body and returns it ONLY as the
// ETag response header, never a body field, and that a list response
// carries no per-item ETag. json.Unmarshal into a map naturally excludes
// a list (array) response without a separate type check: an array fails
// to unmarshal into map[string]any and this returns respBody unchanged.
//
// No-op for any command outside credentialCommandFamilies, and for a
// response with no ETag header.
//
// CALL ORDER, deliberately: executor.go runs this BEFORE
// guardCredentialResponse, not after. Finding 1193: a response's ETag
// header is server-controlled data the CLI has not examined at all; the
// previous order ran the guard first and spliced the ETag in
// afterwards, so a secret-shaped (or otherwise malicious) ETag value
// reached printable output with no scan ever looking at it. Injecting
// first means the guard's structured scan sees the "etag" key like any
// other field and refuses it the same way it would refuse that same
// text anywhere else in the body.
//
// Returns an error, and never respBody, for a secret-shaped ETag
// (finding 1215, G2): this function validates the value it is about to
// insert on its own, rather than relying solely on the guard running
// afterward, so this contract holds even if a future caller changes
// that order.
//
// Finding 1215 (G2): json.Unmarshal of a body that is the literal JSON
// value `null` succeeds with item left nil (Go's documented behaviour
// for unmarshalling null into a map), and the unchecked `item["etag"]
// = etag` below then panicked assigning into a nil map, crashing before
// the guard or the --json error envelope ever ran. The nil check treats
// `null` the same as every other non-object body (an array, a bare
// string, a malformed body): nothing to inject an etag field into, so
// respBody passes through unchanged for guardCredentialResponse to
// examine on its own terms.
func injectETagIntoCredentialResponse(cmdDef manifest.Command, respBody []byte, headers http.Header) ([]byte, error) {
	if _, _, ok := credentialFamilyFor(cmdDef.Command); !ok {
		return respBody, nil
	}
	etag := strings.TrimSpace(headers.Get("ETag"))
	if etag == "" {
		return respBody, nil
	}
	if runosSecretPattern.MatchString(etag) {
		return nil, fmt.Errorf("refusing to render response: the response's ETag header was a bearer-secret-shaped value, which the inventory contract forbids")
	}
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil || item == nil {
		return respBody, nil
	}
	item["etag"] = etag
	sanitized, err := json.Marshal(item)
	if err != nil {
		return respBody, nil
	}
	return sanitized, nil
}

// printOneShotBanner prints the one-shot-secret banner for a successful
// issuance response, chosen by the command's registered family, or does
// nothing for an inventory command or a command outside
// credentialCommandFamilies. Replaces two hardcoded command-literal
// checks in executor.go (finding 1150's `add-personal` instance: the old
// checks named only "account/api-keys/add", so the personal-key issuance
// command got no banner at all once its field stopped being suppressed
// from the table).
func printOneShotBanner(w io.Writer, cmdDef manifest.Command, respBody []byte) {
	family, verb, ok := credentialFamilyFor(cmdDef.Command)
	if !ok || !family.issuanceVerbs[verb] {
		return
	}
	switch family.secretField {
	case "token":
		printApiKeyTokenBanner(w, respBody)
	case "keyValue":
		printNotifyKeyBanner(w, respBody)
	case "secret":
		printTypedCredentialSecretBanner(w, respBody)
	}
}

// printApiKeyTokenBanner prints an ASCII-framed reminder under the
// formatter's table output for `account api-keys add`/`add-personal`.
// Surfaces the one-shot token + warning so terminal users don't miss
// them in a busy session. Silently no-ops when the response body lacks
// a token (e.g. when the conductor returned a partial response shape)
// to avoid blowing up the call site. Regression target: I11-F.
func printApiKeyTokenBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	token, _ := item["token"].(string)
	if token == "" {
		return
	}
	warning, _ := item["warning"].(string)
	if warning == "" {
		warning = "This token will not be shown again. Store it in a secret manager."
	}
	printOneShotSecretBanner(w, "ONE-SHOT TOKEN - save it now, it cannot be retrieved", token, warning)
}

// printNotifyKeyBanner prints the same banner shape as
// printApiKeyTokenBanner for `account notify-keys add`. Notify keys are
// the symmetric notification-webhook credential and have the same
// store-the-hash, return-the-secret-once contract. Silently no-ops
// when the response body lacks `keyValue` so a partial conductor shape
// can't blow up the call site. Regression target: I12-H (mirror of
// I11-F for PATs).
func printNotifyKeyBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	keyValue, _ := item["keyValue"].(string)
	if keyValue == "" {
		return
	}
	printOneShotSecretBanner(w, "ONE-SHOT NOTIFY KEY - save it now, it cannot be retrieved", keyValue,
		"This key will not be shown again. Store it in a secret manager.")
}

// printTypedCredentialSecretBanner mirrors printApiKeyTokenBanner for
// the typed personal-access-tokens/account-api-keys families' `secret`
// field (Roid's writePATWithSecret/writeAccountKeyWithSecret shape). Not
// reachable through any published command yet (see
// credentialCommandFamilies), but kept alongside its siblings so
// printOneShotBanner needs no further change once Conductor publishes
// add/rotate.
func printTypedCredentialSecretBanner(w io.Writer, respBody []byte) {
	var item map[string]any
	if err := json.Unmarshal(respBody, &item); err != nil {
		return
	}
	secret, _ := item["secret"].(string)
	if secret == "" {
		return
	}
	printOneShotSecretBanner(w, "ONE-SHOT SECRET - save it now, it cannot be retrieved", secret,
		"This secret will not be shown again. Store it in a secret manager.")
}

// printOneShotSecretBanner is the shared ASCII-framed rendering all
// three one-shot banners use.
func printOneShotSecretBanner(w io.Writer, title, secret, warning string) {
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  "+title)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+secret)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+warning)
	fmt.Fprintln(w, bar)
}
