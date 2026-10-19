package cmd

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// accessSubject parses kind:id for a grant's subject.
func accessSubject(s string) (map[string]string, error) {
	kind, id, ok := strings.Cut(s, ":")
	if !ok || id == "" || (kind != "person" && kind != "group" && kind != "key") {
		return nil, fmt.Errorf("subject %q: use person:<id>, group:<id> or key:<id>", s)
	}
	return map[string]string{"kind": kind, "id": id}, nil
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
	subject, role, scope  string
	actions, sourceRanges []string
	window, minAssurance  string
	requireMFA            bool
	notBefore, expires    string
	expiresIn             time.Duration
}

// grantBody builds a grant request from the flags.
func grantBody(acct string, f grantFlags, now time.Time) (map[string]any, error) {
	subject, err := accessSubject(f.subject)
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
