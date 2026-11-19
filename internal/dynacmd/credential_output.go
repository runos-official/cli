package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		`|rk_[A-Za-z0-9_-]{43}`,
)

// credentialFamily names the one JSON field a credential command family's
// issuing operations may carry a bearer secret under, and which verbs
// (the manifest command's final path segment) are issuing operations.
// Every other verb in the family is pure inventory: no secret-shaped
// value is permitted anywhere in its response.
type credentialFamily struct {
	secretField   string
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
// "account/personal-access-tokens" and "account/account-api-keys" are
// not published in the CLI manifest yet (Story 342 / FPL434 is still in
// progress), so no live command reaches this guard under those prefixes
// today. Their field name and verbs are taken from Roid's actual shipped
// response shape (writePATWithSecret / writeAccountKeyWithSecret,
// roid/internal/server/personalaccesstokens.go and accountapikeys.go,
// commit da5fdcbd97c4), which Conductor's relay forwards unchanged
// (story 382, FPL520): both write `m["secret"] = secret` on create and
// rotate only. If Story 342 ships different sub-command spellings,
// update issuanceVerbs here; the prefixes themselves match the family
// names FPL434 already documents.
var credentialCommandFamilies = map[string]credentialFamily{
	"account/api-keys": {
		secretField:   "token",
		issuanceVerbs: map[string]bool{"add": true, "personal": true},
	},
	"account/notify-keys": {
		secretField:   "keyValue",
		issuanceVerbs: map[string]bool{"add": true},
	},
	"account/personal-access-tokens": {
		secretField:   "secret",
		issuanceVerbs: map[string]bool{"add": true, "rotate": true},
	},
	"account/account-api-keys": {
		secretField:   "secret",
		issuanceVerbs: map[string]bool{"add": true, "rotate": true},
	},
}

// credentialFamilyFor splits a manifest command path into its family
// (every segment but the last) and verb (the last segment), and reports
// whether that family is a registered credential family. A command
// outside this registry reports ok=false, and guardCredentialResponse
// does not scan it at all.
func credentialFamilyFor(command string) (family credentialFamily, verb string, ok bool) {
	idx := strings.LastIndex(command, "/")
	if idx < 0 {
		return credentialFamily{}, "", false
	}
	prefix, verb := command[:idx], command[idx+1:]
	family, ok = credentialCommandFamilies[prefix]
	return family, verb, ok
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
// top-level field, and refuses it anywhere else, including a second
// occurrence under a different key or nested inside an object or array
// (finding 1105: field name alone was previously sufficient to exempt a
// value anywhere in the body, which is not what "the one-shot field"
// means).
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
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil // not JSON; nothing this guard can check
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
		if allowedField != "" && key == allowedField {
			if s, ok := value.(string); ok && runosSecretPattern.MatchString(s) {
				continue // the one legitimate carrier, on the one operation documented to return it
			}
		}
		if found := findSecretValue(value, key); found != "" {
			return fmt.Errorf("refusing to render response: a bearer-secret-shaped value was found in field %q, which the inventory contract forbids", found)
		}
	}
	return nil
}

// findSecretValue walks v looking for a string matching
// runosSecretPattern. path names the current field for the error
// message.
func findSecretValue(v any, path string) string {
	switch t := v.(type) {
	case string:
		if runosSecretPattern.MatchString(t) {
			return path
		}
	case map[string]any:
		for k, nested := range t {
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

// printApiKeyTokenBanner prints an ASCII-framed reminder under the
// formatter's table output for `account api-keys add`. Surfaces the
// one-shot token + warning so terminal users don't miss them in a busy
// session. Silently no-ops when the response body lacks a token (e.g.
// when the conductor returned a partial response shape) to avoid
// blowing up the call site. Regression target: I11-F.
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
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT TOKEN - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+token)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+warning)
	fmt.Fprintln(w, bar)
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
	const bar = "============================================================"
	fmt.Fprintln(w)
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w, "  ONE-SHOT NOTIFY KEY - save it now, it cannot be retrieved")
	fmt.Fprintln(w, bar)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+keyValue)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  This key will not be shown again. Store it in a secret manager.")
	fmt.Fprintln(w, bar)
}
