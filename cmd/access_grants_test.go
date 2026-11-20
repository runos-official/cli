package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidapi"
)

// grantStore is a tiny fake identity service that serves the grants
// routes only, keeping a separate grant map per account so a test can
// prove one account's withdrawal never touches another's.
type grantStore struct {
	mu        sync.Mutex
	byAccount map[string]map[string]bool // account -> grant id -> exists
	calls     []string
}

func newGrantServer(t *testing.T, store *grantStore) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		store.mu.Lock()
		store.calls = append(store.calls, r.Method+" "+r.URL.Path)
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/accounts/"), "/")
		w.Header().Set("Content-Type", "application/json")
		if len(parts) != 4 || parts[1] != "access" || parts[2] != "grants" {
			store.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
			return
		}
		account, grantID := parts[0], parts[3]
		exists := store.byAccount[account][grantID]
		switch {
		case r.Method == http.MethodGet && exists:
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"` + grantID + `"}`))
		case r.Method == http.MethodDelete && exists:
			delete(store.byAccount[account], grantID)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
		}
		store.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Finding 1333's integration half: one key granted in two accounts,
// withdrawing in the first must send the local DELETE only for that
// account, leave the second account's grant intact, carry the existing
// If-Match behaviour, and never touch any global-revoke-shaped route.
func TestWithdrawalIsLocalToTheNamedAccountAndNeverCallsGlobalRevoke(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	store := &grantStore{byAccount: map[string]map[string]bool{
		"a1": {"g1": true},
		"a2": {"g1": true},
	}}
	srv := newGrantServer(t, store)
	cfg := &config.Config{AuthIssuer: srv.URL, ConductorURL: srv.URL, Roid: &config.RoidSignIn{Issuer: srv.URL, PersonID: "p1"}}
	client, err := accessClient(cfg, func() (string, error) { return "at_x", nil })
	if err != nil {
		t.Fatal(err)
	}

	a1 := &accessTarget{client: client, acct: "a1"}
	if _, err := a1.do(roidapi.Call{Op: "revokeGrant", Params: map[string]string{"grant": "g1"}, IfMatch: `"v1"`}); err != nil {
		t.Fatalf("withdraw in a1: %v", err)
	}

	a2 := &accessTarget{client: client, acct: "a2"}
	if _, err := a2.do(roidapi.Call{Op: "getGrant", Params: map[string]string{"grant": "g1"}}); err != nil {
		t.Fatalf("a2's grant should still be there: %v", err)
	}

	store.mu.Lock()
	calls := append([]string(nil), store.calls...)
	remainingA1, remainingA2 := len(store.byAccount["a1"]), len(store.byAccount["a2"])
	store.mu.Unlock()

	if remainingA1 != 0 {
		t.Errorf("a1 still has %d grant(s) after withdrawal", remainingA1)
	}
	if remainingA2 != 1 {
		t.Errorf("a2 has %d grant(s), want the one grant left untouched", remainingA2)
	}
	for _, call := range calls {
		if !strings.Contains(call, "/access/grants/") {
			t.Errorf("unexpected call outside the local grants route: %q", call)
		}
		if strings.Contains(call, "api-keys") || strings.Contains(call, "personal-access-tokens") {
			t.Errorf("a call reached a global credential-lifecycle route: %q", call)
		}
	}
	want := []string{"DELETE /v1/accounts/a1/access/grants/g1", "GET /v1/accounts/a2/access/grants/g1"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Errorf("calls = %v, want %v", calls, want)
	}
}

// Finding 1334/1361: a structured refusal from an ordinary (non-step-up,
// non-412) grant mutation must keep its code, reason, action, scope,
// requirement, decision_id and message under --json, not only
// accessRefusal's human sentence.
func TestAccessJSONErrorEnvelopePreservesStructuredFields(t *testing.T) {
	err := &roidapi.APIError{Status: 403, RefusalFields: roidapi.RefusalFields{
		Code: "delegation.escalation", Reason: "scope", Action: "apps.deploy", Scope: "acct/a1/cluster/c1",
		Requirement: "fresh_sign_in", DecisionID: "d_1", Message: "no grant covers this",
		Policies: []byte(`["acct-policy-1"]`),
	}}
	env := accessJSONErrorEnvelope(err)
	if env["code"] != "delegation.escalation" || env["reason"] != "scope" || env["action"] != "apps.deploy" || env["scope"] != "acct/a1/cluster/c1" {
		t.Fatalf("envelope = %v", env)
	}
	if env["requirement"] != "fresh_sign_in" || env["decision_id"] != "d_1" || env["message"] != "no grant covers this" {
		t.Fatalf("envelope = %v", env)
	}
	// Finding 1382: Roid supplies blocking policy IDs under "policies"
	// for a missing_context refusal (e.g. a policy an administrator must
	// remove); the --json envelope must carry them too.
	if !strings.Contains(string(env["policies"].(json.RawMessage)), "acct-policy-1") {
		t.Fatalf("envelope policies = %v", env["policies"])
	}
	if env["status"] != 403 {
		t.Fatalf("envelope status = %v", env["status"])
	}
}

// Finding 1334/1361: a stale ETag (412) must keep the producer's
// code/reason too, not collapse to the bare sentinel with no structured
// fields.
func TestAccessJSONErrorEnvelopePreservesStaleFields(t *testing.T) {
	err := &roidapi.StaleError{RefusalFields: roidapi.RefusalFields{Code: "precondition_failed", Reason: "etag"}}
	env := accessJSONErrorEnvelope(err)
	if env["code"] != "precondition_failed" || env["reason"] != "etag" || env["status"] != http.StatusPreconditionFailed {
		t.Fatalf("envelope = %v", env)
	}
}

// Finding 1361: a step-up challenge's own code and decision_id must
// reach the --json envelope too, not only step_up_id/step_up_url/action.
func TestAccessJSONErrorEnvelopePreservesStepUpFields(t *testing.T) {
	err := &roidapi.StepUpRequiredError{ID: "su_1", URL: "https://id.example.com/stepup/su_1", RefusalFields: roidapi.RefusalFields{
		Code: "authz.needs", Action: "apps.deploy", DecisionID: "d_3",
	}}
	env := accessJSONErrorEnvelope(err)
	if env["code"] != "authz.needs" || env["action"] != "apps.deploy" || env["decision_id"] != "d_3" {
		t.Fatalf("envelope = %v", env)
	}
	if env["step_up_id"] != "su_1" || env["step_up_url"] != "https://id.example.com/stepup/su_1" || env["requirement"] != "step_up" {
		t.Fatalf("envelope = %v", env)
	}
}
