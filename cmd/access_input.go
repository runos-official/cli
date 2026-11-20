package cmd

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// accessSubject parses kind:id for a grant's subject (person, group or the
// legacy key kind). It never accepts account_api_key: that principal kind
// is selected only through --key, which validates the public credential id
// grammar instead of trusting a free-form kind:id pair (see grantSubject).
func accessSubject(s string) (map[string]string, error) {
	kind, id, ok := strings.Cut(s, ":")
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
		if err := validatePublicCredentialID(f.keyID); err != nil {
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
