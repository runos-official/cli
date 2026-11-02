package kubeaccess

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/roidauth"
)

func TestPlainMessageNamesTheFixForEachRoidRefusal(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"no grant", &Refusal{Status: 403, Code: "authz.denied", Reason: "no_grant", Message: "internal words"},
			[]string{"no Kubernetes access", "account admin"}},
		{"deny listed", &Refusal{Status: 403, Code: "authz.denied", Reason: "deny_listed", Message: "Your Kubernetes access was cut. Try again after 2026-11-02T10:16:00Z."},
			[]string{"cut", "Try again after 2026-11-02T10:16:00Z", "account admin"}},
		{"deny listed without roid's words", &Refusal{Status: 403, Code: "authz.denied", Reason: "deny_listed"},
			[]string{"cut", "try again in a few minutes"}},
		{"grant ends now", &Refusal{Status: 403, Code: "authz.denied", Reason: "grant_ends_now"},
			[]string{"ends now"}},
		{"engine refusal", &Refusal{Status: 403, Code: "authz.denied", Reason: "policy_says_no", Message: "Not allowed here."},
			[]string{"refused", "Not allowed here."}},
		{"unenforceable", &Refusal{Status: 403, Code: "policy.unenforceable", Reason: "unenforceable_grant", Message: "Grant g1 has a time window."},
			[]string{"cannot be turned into Kubernetes roles", "Grant g1 has a time window."}},
		{"invalid policy", &Refusal{Status: 403, Code: "policy.invalid", Reason: "invalid_policy", Message: "Grant g2 is broken."},
			[]string{"do not compile", "Grant g2 is broken."}},
		{"old sign-in", &Refusal{Status: 403, Code: "authz.needs", Reason: "fresh_sign_in", Requirement: "fresh_sign_in"},
			[]string{"sign-in is too old", "runos login"}},
		{"step up cannot be done by a token request", &Refusal{Status: 403, Code: "authz.needs", Reason: "tokens_open_no_step_up", Requirement: "step_up"},
			[]string{"security-key check", "cannot do that", "account admin"}},
		{"approval cannot be done by a token request", &Refusal{Status: 403, Code: "authz.needs", Reason: "tokens_open_no_step_up", Requirement: "approval"},
			[]string{"approval", "cannot do that", "account admin"}},
		{"not ready no applier", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "no_applier"},
			[]string{"not ready", "applier", "install"}},
		{"not ready applier unconfirmed", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "applier_unconfirmed"},
			[]string{"not ready", "wait a minute", "check the applier"}},
		{"not ready retired", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "retired"},
			[]string{"not ready", "left the account", "--cluster"}},
		{"not ready deny only", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "deny_only"},
			[]string{"not ready", "grant", "account admin"}},
		{"not ready no namespace", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "no_managed_namespace"},
			[]string{"not ready", "runos.com/roid-access=true", "account admin"}},
		{"not ready unknown reason", &Refusal{Status: 409, Code: "k8s.not_ready", Reason: "something_new"},
			[]string{"not ready", "account admin"}},
		{"api host does not serve the route", &Refusal{Status: 404, Code: "not_found", Reason: "not_on_this_host"},
			[]string{"does not serve Kubernetes access", "runos config get", "RUNOS_API_URL"}},
		{"route only reachable through the gateway", &Refusal{Status: 404, Code: "not_found", Reason: "not_via_rapi"},
			[]string{"does not serve Kubernetes access", "runos config get"}},
		{"class refused", &Refusal{Status: 403, Code: "exchange.class_refused"},
			[]string{"signed-in person", "runos login"}},
		{"session ended", &Refusal{Status: 401, Code: "auth.session_expired"},
			[]string{"sign-in", "runos login"}},
		{"revoked", &Refusal{Status: 401, Code: "auth.revoked"},
			[]string{"runos login"}},
		{"unknown cluster", &Refusal{Status: 404, Code: "not_found"},
			[]string{"does not hold", "--cluster"}},
		{"bad request", &Refusal{Status: 400, Code: "invalid_request", Reason: "cluster"},
			[]string{"cluster"}},
		{"unavailable", &Refusal{Status: 503, Code: "audit.unavailable"},
			[]string{"try again"}},
		{"rate limit", &Refusal{Status: 429, RetryAfter: "12"},
			[]string{"12 seconds"}},
		{"unknown", &Refusal{Status: 418, Code: "teapot", Reason: "short"},
			[]string{"418", "teapot"}},
		{"not signed in", roidauth.ErrNotSignedIn, []string{"not signed in", "runos login"}},
		{"session ended local", fmt.Errorf("x: %w", roidauth.ErrSessionEnded), []string{"sign-in has ended", "runos login"}},
		{"network", &TransportError{Err: errors.New("request failed: dial tcp: refused")}, []string{"Could not reach", "dial tcp"}},
		{"other", errors.New("credential store locked"), []string{"Could not get a Kubernetes token", "credential store locked"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PlainMessage(c.err)
			for _, w := range c.want {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(w)) {
					t.Errorf("message %q lacks %q", got, w)
				}
			}
			if strings.ContainsAny(got, "{}") || strings.Contains(got, "\n") {
				t.Errorf("message must be one plain line, got %q", got)
			}
		})
	}
}

func TestNoGrantHidesRoidsOwnMessage(t *testing.T) {
	// The route answers a non-member and a member without a grant alike so it
	// does not tell outsiders which clusters exist. The CLI must not add detail.
	got := PlainMessage(&Refusal{Status: 403, Code: "authz.denied", Reason: "no_grant", Message: "cluster-xyz is real"})
	if strings.Contains(got, "cluster-xyz") {
		t.Errorf("no_grant message leaked roid detail: %q", got)
	}
}

func TestRefusalErrorIsReadable(t *testing.T) {
	e := &Refusal{Status: 403, Code: "authz.denied", Reason: "no_grant"}
	if !strings.Contains(e.Error(), "403") || !strings.Contains(e.Error(), "no_grant") {
		t.Errorf("Error() = %q", e.Error())
	}
}
