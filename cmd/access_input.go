package cmd

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
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
func containsABearerSecretShape(v string) bool {
	return strings.Contains(v, "runos_pat_") || strings.Contains(v, "runos_key_") || strings.HasPrefix(v, "rk_")
}

// containsABearerSecretShapeInSubject is containsABearerSecretShape for
// a kind:id (or bare) subject value. The runos_pat_/runos_key_ shapes
// never contain ':', so checking the whole value already catches them
// wherever they sit; the rk_ check is deliberately ANCHORED to the
// START of a value (validatePublicCredentialID's own public ids may
// legitimately contain "rk_" mid-string, e.g. as part of a scope-like
// segment), so a BARE rk_... value matches containsABearerSecretShape
// directly, but "key:rk_..." or "person:rk_..." does not: the anchor
// lands on "key"/"person", not on the secret after the colon. Checking
// the part after a ':', if any, too is what catches that case.
func containsABearerSecretShapeInSubject(v string) bool {
	if containsABearerSecretShape(v) {
		return true
	}
	if _, id, ok := strings.Cut(v, ":"); ok {
		return containsABearerSecretShape(id)
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
	if containsABearerSecretShapeInSubject(v) {
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
	if containsABearerSecretShapeInSubject(s) {
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

// grantBody builds a grant request from the flags.
func grantBody(acct string, f grantFlags, now time.Time) (map[string]any, error) {
	subject, err := grantSubject(f)
	if err != nil {
		return nil, err
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
