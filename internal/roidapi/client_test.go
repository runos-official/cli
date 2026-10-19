package roidapi

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
)

func staticToken(tok string) func() (string, error) {
	return func() (string, error) { return tok, nil }
}

func TestBaseURLIsTheIssuerAndRefusesANonRoidEnvironment(t *testing.T) {
	got, err := BaseURL(&config.Config{AuthIssuer: "https://id.example.com/"})
	if err != nil || got != "https://id.example.com" {
		t.Fatalf("BaseURL = %q, %v", got, err)
	}
	if _, err := BaseURL(&config.Config{}); !errors.Is(err, ErrNotRoid) {
		t.Fatalf("a Firebase environment: err = %v, want ErrNotRoid", err)
	}
}

func TestAReadGoesToRoidWithTheBearer(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	res, err := c.Do(Call{Op: "listRoles", Params: map[string]string{"acct": "a1"}})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("listRoles: %v %v", res, err)
	}
	if f.calls[0] != "GET /v1/accounts/a1/access/roles" {
		t.Fatalf("call = %q", f.calls[0])
	}
}

func TestPathParametersAreEscaped(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	_, _ = c.Do(Call{Op: "getRole", Params: map[string]string{"acct": "a1", "role": "x/../y"}})
	if f.calls[0] != "GET /v1/accounts/a1/access/roles/x%2F..%2Fy" {
		t.Fatalf("call = %q, want the role escaped as one path segment", f.calls[0])
	}
}

func TestAMissingPathParameterIsRefusedBeforeAnyCall(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	if _, err := c.Do(Call{Op: "getRole", Params: map[string]string{"acct": "a1"}}); err == nil {
		t.Fatal("a call without the role parameter was sent")
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestAWriteWithoutAnETagReadsFirstAndSendsIfMatch(t *testing.T) {
	f, srv := newFakeRoid(t)
	f.roles["ops"] = &fakeVersioned{version: 3, body: map[string]any{"id": "ops", "name": "Ops", "builtin": false, "actions": []string{"clusters.read"}}}
	c := New(srv.URL, staticToken("at_test"))
	res, err := c.Do(Call{Op: "updateRole", Params: map[string]string{"acct": "a1", "role": "ops"},
		Body: map[string]any{"name": "Operators", "actions": []string{"clusters.read"}}})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("updateRole: %v %v", res, err)
	}
	if len(f.calls) != 2 || f.calls[0] != "GET /v1/accounts/a1/access/roles/ops" || f.calls[1] != "PUT /v1/accounts/a1/access/roles/ops" {
		t.Fatalf("calls = %v", f.calls)
	}
	if got := f.headers[1].Get("If-Match"); got != `"v3"` {
		t.Fatalf("If-Match = %q", got)
	}
}

func TestAStaleETagIsErrStale(t *testing.T) {
	f, srv := newFakeRoid(t)
	f.grants["g_9"] = &fakeVersioned{version: 2, body: map[string]any{"id": "g_9"}}
	c := New(srv.URL, staticToken("at_test"))
	_, err := c.Do(Call{Op: "revokeGrant", Params: map[string]string{"acct": "a1", "grant": "g_9"}, IfMatch: `"v1"`})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("the message does not say what happened: %v", err)
	}
}

func TestGroupMembershipChangesCarryTheGroupETag(t *testing.T) {
	f, srv := newFakeRoid(t)
	f.groups["ops"] = &fakeVersioned{version: 4, body: map[string]any{"id": "ops", "name": "Ops", "members": []string{}, "idp_managed": false}}
	c := New(srv.URL, staticToken("at_test"))
	res, err := c.Do(Call{Op: "addGroupMember", Params: map[string]string{"acct": "a1", "group": "ops", "person": "p_bob"}})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("addGroupMember: %v %v", res, err)
	}
	if f.calls[0] != "GET /v1/accounts/a1/access/groups/ops" || f.headers[1].Get("If-Match") != `"v4"` {
		t.Fatalf("calls = %v, If-Match = %q", f.calls, f.headers[1].Get("If-Match"))
	}
}

func TestAnEscalationNeedsAStepUpAndTheRetryCarriesIt(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	call := Call{Op: "createGrant", Params: map[string]string{"acct": "a1"},
		Body: map[string]any{"subject": map[string]string{"kind": "person", "id": "p_bob"}, "role": "admin", "scope": "acct/a1"}}
	_, err := c.Do(call)
	var su *StepUpRequiredError
	if !errors.As(err, &su) || su.ID != "su_1" {
		t.Fatalf("err = %v, want a step-up with id su_1", err)
	}
	f.stepUps["su_1"] = true // the person completes it with their security key
	call.StepUp = su.ID
	res, err := c.Do(call)
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("retry: %v %v", res, err)
	}
	if f.headers[1].Get("X-RunOS-Step-Up") != "su_1" {
		t.Fatalf("the retry did not carry the step-up")
	}
}

func TestARefusalCarriesRoidsCodeAndReason(t *testing.T) {
	_, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	_, err := c.Do(Call{Op: "getGrant", Params: map[string]string{"acct": "a1", "grant": "nope"}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestQueryParametersReachRoid(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	_, err := c.Do(Call{Op: "getEffectiveAccess", Params: map[string]string{"acct": "a1"},
		Query: url.Values{"person": {"p_bob"}, "scope": {"acct/a1/cluster/c1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.calls[0], "GET /v1/accounts/a1/access/effective") {
		t.Fatalf("call = %q", f.calls[0])
	}
}

func TestATokenErrorStopsTheCall(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, func() (string, error) { return "", errors.New("not signed in") })
	if _, err := c.Do(Call{Op: "listRoles", Params: map[string]string{"acct": "a1"}}); err == nil {
		t.Fatal("a call went out without a token")
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}
