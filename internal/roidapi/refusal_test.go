package roidapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
)

// Repeat finding 1361 (earlier: 1334). The rule: every terminal refusal
// must carry forward EVERY applicable field the producer's response
// held (code, reason, detail, action, scope, requirement, decision_id,
// message, policies), in BOTH the structured fields a --json caller
// reads AND the text Error() renders, regardless of which refusal TYPE
// refusal() chose. Before this fix, each of APIError, StepUpRequiredError
// and StaleError carried its OWN narrower subset of these fields, so
// whichever field a given type's struct or Error() method omitted was
// lost for every refusal of that type, not only the one case a prior
// probe happened to exercise.

// A step-up challenge: the producer's own code, action and decision_id
// must survive, in the struct (for --json) and in Error() (for text).
func TestRefusal_StepUpChallenge_CarriesProducerCodeActionAndDecisionID(t *testing.T) {
	body := `{"error":{"code":"authz.needs","requirement":"step_up","step_up_id":"su_1","step_up_url":"https://id.example.com/stepup/su_1","action":"apps.deploy","decision_id":"d_1"}}`
	err := refusal(&api.Result{StatusCode: http.StatusForbidden, Body: []byte(body)})
	var su *StepUpRequiredError
	if !errors.As(err, &su) {
		t.Fatalf("err = %v, want *StepUpRequiredError", err)
	}
	if su.ID != "su_1" || su.URL != "https://id.example.com/stepup/su_1" {
		t.Fatalf("su = %+v", su)
	}
	if su.Code != "authz.needs" {
		t.Errorf("Code = %q, want authz.needs", su.Code)
	}
	if su.Action != "apps.deploy" {
		t.Errorf("Action = %q, want apps.deploy", su.Action)
	}
	if su.DecisionID != "d_1" {
		t.Errorf("DecisionID = %q, want d_1", su.DecisionID)
	}
	if !strings.Contains(err.Error(), "apps.deploy") {
		t.Errorf("Error() = %q, want it to name the refused action", err.Error())
	}
}

// A fresh_sign_in challenge is NOT a step-up (no step_up_id), so it
// falls to the ordinary APIError branch. requirement and decision_id
// must survive there too, in the struct and in Error() text.
func TestRefusal_FreshSignIn_CarriesRequirementAndDecisionID(t *testing.T) {
	body := `{"error":{"code":"auth.needs_fresh_sign_in","requirement":"fresh_sign_in","decision_id":"d_2","reason":"sign_in_too_old"}}`
	err := refusal(&api.Result{StatusCode: http.StatusForbidden, Body: []byte(body)})
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.Requirement != "fresh_sign_in" {
		t.Errorf("Requirement = %q, want fresh_sign_in", ae.Requirement)
	}
	if ae.DecisionID != "d_2" {
		t.Errorf("DecisionID = %q, want d_2", ae.DecisionID)
	}
	if !strings.Contains(err.Error(), "fresh_sign_in") {
		t.Errorf("Error() = %q, want it to name the requirement", err.Error())
	}
}

// A stale ETag (412) must keep its producer code in TEXT, not only in
// the struct: finding 1361 found the struct already carried Code but
// Error() never printed it.
func TestRefusal_Stale412_CarriesCodeInTextToo(t *testing.T) {
	body := `{"error":{"code":"precondition_failed","reason":"etag"}}`
	err := refusal(&api.Result{StatusCode: http.StatusPreconditionFailed, Body: []byte(body)})
	var se *StaleError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StaleError", err)
	}
	if se.Code != "precondition_failed" {
		t.Errorf("Code = %q, want precondition_failed", se.Code)
	}
	if !strings.Contains(err.Error(), "precondition_failed") {
		t.Errorf("Error() = %q, want it to name the code precondition_failed", err.Error())
	}
	if !errors.Is(err, ErrStale) {
		t.Errorf("errors.Is(err, ErrStale) failed: %v", err)
	}
}

// An ordinary policy denial must keep message and policies too, not
// only code/reason/detail/action/scope.
func TestRefusal_OrdinaryDenial_CarriesMessageAndPolicies(t *testing.T) {
	body := `{"error":{"code":"policy.denied","reason":"no_grant","message":"no grant covers this action","policies":["acct-policy-1","acct-policy-2"]}}`
	err := refusal(&api.Result{StatusCode: http.StatusForbidden, Body: []byte(body)})
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if ae.Message != "no grant covers this action" {
		t.Errorf("Message = %q", ae.Message)
	}
	if len(ae.Policies) == 0 || !strings.Contains(string(ae.Policies), "acct-policy-1") {
		t.Errorf("Policies = %q", ae.Policies)
	}
}
