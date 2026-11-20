package cmd

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"
)

// containsABearerSecretShape reports whether v contains or matches any
// currently published RunOS/Roid bearer secret shape: the typed
// runos_pat_/runos_key_ shape and the legacy Conductor runos_pat_ shape
// (the same unanchored substring check catches both), or Roid's own
// legacy rk_ prefix. Finding 1383: --subject's generic kind:id form has
// no positive grammar of its own to check a value against (unlike
// --key's public-id grammar), so this is the one floor every value that
// could become a grant subject id, selector, or list filter must still
// clear, whatever kind it claims to be.
//
// Findings 1410 and 1428 (same cause, twice): the rk_ check is anchored
// to avoid false-flagging a public id that legitimately contains "rk_"
// mid-string (validatePublicCredentialID's own accepted "pat_work_rk_1"
// case), but anchoring to the raw string's start is defeated by ANY
// text in front of the secret -- whitespace (1410), then a second kind
// prefix, a "Bearer " label, or any other token (1428). Normalizing one
// wrapper shape at a time (TrimSpace, then a single ':' split) just
// reopens the same hole for the next wrapper. The fix is a grammar, not
// another wrapper: split v on every ':' and every run of Unicode
// whitespace -- the two separators a kind:id subject or a list filter
// can legitimately use -- and anchor the rk_ check to each resulting
// TOKEN instead of to the whole string. A legitimate public id with no
// ':' or whitespace in it is exactly one token, so "pat_work_rk_1"
// still isn't anchored on "rk_" (it starts with "pat_"); any wrapping
// text around a real rk_... secret becomes its own token, leaving the
// secret as a token that DOES start with "rk_", however many layers of
// "kind:", "Bearer " or stray words surround it.
func containsABearerSecretShape(v string) bool {
	if strings.Contains(v, "runos_pat_") || strings.Contains(v, "runos_key_") {
		return true
	}
	for _, token := range strings.FieldsFunc(v, func(r rune) bool {
		return r == ':' || unicode.IsSpace(r)
	}) {
		if strings.HasPrefix(token, "rk_") {
			return true
		}
	}
	return false
}

var errSubjectIsABearerSecret = errors.New("subject: takes a public id, never a bearer secret -- use --key for an Account API Key by its public id, or the id the identity service shows for a person or group")

// validateListSubjectFilter refuses a bearer-secret-shaped --subject
// filter on 'access grants list' before it reaches the query string
// (finding 1383): the filter is a free-form passthrough (it also
// carries the new "account_api_key:<id>" form, which has no fixed kind
// list to validate against, unlike create's --subject kind:id), so
// this is the one check every value must still clear.
func validateListSubjectFilter(v string) error {
	if containsABearerSecretShape(v) {
		return errSubjectIsABearerSecret
	}
	return nil
}

// accessSubject parses kind:id for a grant's subject (person, group or the
// legacy key kind). It never accepts account_api_key: that principal kind
// is selected only through --key, which validates the public credential id
// grammar instead of trusting a free-form kind:id pair (see grantSubject).
//
// Checked, and refused with a canned message that never echoes the input,
// before the parsed result is used to build any other error (finding
// 1383): a bare bearer with no ':' used to fail only the kind check
// below, whose error message echoes the whole input back; a kind:id
// pair whose id was itself bearer-shaped used to pass straight through
// as a legitimate person/group/key id, headed for the request body or a
// query filter.
func accessSubject(s string) (map[string]string, error) {
	kind, id, ok := strings.Cut(s, ":")
	if containsABearerSecretShape(s) {
		return nil, errSubjectIsABearerSecret
	}
	if !ok || id == "" || (kind != "person" && kind != "group" && kind != "key") {
		return nil, fmt.Errorf("subject %q: use person:<id>, group:<id> or key:<id>", s)
	}
	return map[string]string{"kind": kind, "id": id}, nil
}

// requireExplicitAccountForAKey refuses an Account API Key grant without
// an explicit, non-blank --account: the key's grants can live in any
// account, so (unlike a person or group subject, which keeps the existing
// fall-back-to-active-account behaviour) the CLI never assumes the active
// one for a machine credential. Mirrors validateExplainSelector's
// equivalent check for 'access explain --credential' (review finding
// 1081: Cobra reports Changed("account") as true for --account="", so
// this inspects the resolved string value, never Changed()).
func requireExplicitAccountForAKey(keyID, account string) error {
	if keyID == "" || account != "" {
		return nil
	}
	return errors.New("granting an Account API Key needs an explicit --account: its grants can live in any account, so the CLI never assumes the active one")
}

// requireExplicitAccount refuses a grant mutation without an explicit,
// non-blank --account: accessSession would otherwise fall back to the
// active account before the request, so an omitted or empty flag could
// act on a DIFFERENT account than the one the caller meant (finding
// 1333, grant withdrawal). Unlike requireExplicitAccountForAKey, this
// applies unconditionally: withdrawal names no subject to inspect, so
// the resource account must always be explicit.
func requireExplicitAccount(account string) error {
	if account != "" {
		return nil
	}
	return errors.New("this needs an explicit --account: the CLI never assumes the active account for a grant mutation")
}

// validatePublicCredentialIDForFlag is validatePublicCredentialID with an
// error that names the flag the CALLER actually used, not --credential's
// (finding 1338 instance 2): access_explain.go's validator has exactly
// one error value, which always names --credential, so reusing it
// directly for --key told a --key user about a flag that does not exist
// on 'access grants create'.
func validatePublicCredentialIDForFlag(flag, v string) error {
	if err := validatePublicCredentialID(v); err != nil {
		return fmt.Errorf("--%s takes a public id (pat_... or key_...), never a secret: use the id the identity service shows for it, not the token itself", flag)
	}
	return nil
}

// grantSubject resolves a grant's subject from the flags: --key selects an
// Account API Key by its public credential id (the canonical account_api_key
// principal kind; never inferred from a runos_pat_/runos_key_/legacy
// prefix), --subject keeps the existing person/group/key compatibility
// path. The two are mutually exclusive: combining them is ambiguous local
// input, refused before anything is sent.
func grantSubject(f grantFlags) (map[string]string, error) {
	switch {
	case f.subject != "" && f.keyID != "":
		return nil, errors.New("give --subject or --key, not both")
	case f.keyID != "":
		if err := validatePublicCredentialIDForFlag("key", f.keyID); err != nil {
			return nil, err
		}
		return map[string]string{"kind": "account_api_key", "id": f.keyID}, nil
	case f.subject != "":
		return accessSubject(f.subject)
	default:
		return nil, errors.New("give --subject or --key: who or what is this granting to?")
	}
}

// accessScope turns a scope flag into a full scope in this account. Empty
// means the whole account; a short form such as cluster/c1 is placed under
// the account; a full scope must name this account.
func accessScope(acct, s string) (string, error) {
	base := "acct/" + acct
	switch {
	case s == "":
		return base, nil
	case s == base || strings.HasPrefix(s, base+"/"):
		return s, nil
	case strings.HasPrefix(s, "acct/"):
		return "", fmt.Errorf("scope %q is in another account than %s", s, acct)
	default:
		return base + "/" + strings.Trim(s, "/"), nil
	}
}

// accessWindow parses HH:MM-HH:MM into minutes of the day.
func accessWindow(s string) (map[string]int, error) {
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return nil, fmt.Errorf("window %q: use HH:MM-HH:MM", s)
	}
	parse := func(v string) (int, error) {
		t, err := time.Parse("15:04", v)
		if err != nil || len(v) != 5 {
			return 0, fmt.Errorf("window %q: use HH:MM-HH:MM", s)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	a, err := parse(from)
	if err != nil {
		return nil, err
	}
	b, err := parse(to)
	if err != nil {
		return nil, err
	}
	return map[string]int{"start_min": a, "end_min": b}, nil
}

// grantFlags is what 'access grants create' reads from its flags.
type grantFlags struct {
	subject, keyID, role, scope string
	actions, sourceRanges       []string
	window, minAssurance        string
	requireMFA                  bool
	notBefore, expires          string
	expiresIn                   time.Duration
}

// errAccountAPIKeyTakesActionsNotRole is criterion 8 (finding 1387, FCR
// 2628): an Account API Key is an exact-action-only machine principal
// (criterion 2), so --key with --role is refused locally, before any
// request, rather than sent for Roid to refuse. Checked in grantBody,
// which runs before accessTarget.do, so no request is ever sent.
var errAccountAPIKeyTakesActionsNotRole = errors.New("an Account API Key receives explicit actions, never a role: give --action, not --role")

// grantBody builds a grant request from the flags.
func grantBody(acct string, f grantFlags, now time.Time) (map[string]any, error) {
	subject, err := grantSubject(f)
	if err != nil {
		return nil, err
	}
	if f.keyID != "" && f.role != "" {
		return nil, errAccountAPIKeyTakesActionsNotRole
	}
	if (f.role == "") == (len(f.actions) == 0) {
		return nil, errors.New("give --role or --action, not both and not neither")
	}
	scope, err := accessScope(acct, f.scope)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"subject": subject, "scope": scope}
	if f.role != "" {
		body["role"] = f.role
	} else {
		body["actions"] = f.actions
	}
	cond := map[string]any{}
	for _, r := range f.sourceRanges {
		if _, err := netip.ParsePrefix(r); err != nil {
			return nil, fmt.Errorf("source range %q is not a CIDR prefix", r)
		}
	}
	if len(f.sourceRanges) > 0 {
		cond["source_ranges"] = f.sourceRanges
	}
	if f.window != "" {
		w, err := accessWindow(f.window)
		if err != nil {
			return nil, err
		}
		cond["window"] = w
	}
	if f.requireMFA {
		cond["require_mfa"] = true
	}
	if f.minAssurance != "" {
		cond["min_assurance"] = f.minAssurance
	}
	if len(cond) > 0 {
		body["conditions"] = cond
	}
	if f.notBefore != "" {
		body["not_before"] = f.notBefore
	}
	switch {
	case f.expires != "" && f.expiresIn > 0:
		return nil, errors.New("give --expires or --expires-in, not both")
	case f.expires != "":
		body["expires_at"] = f.expires
	case f.expiresIn > 0:
		body["expires_at"] = now.Add(f.expiresIn).UTC().Format(time.RFC3339)
	}
	return body, nil
}
