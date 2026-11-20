package roidapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/runos-official/cli/internal/api"
)

// ErrStale means the resource changed after the CLI read it, so the
// identity service refused the change (412). Read it again and retry.
var ErrStale = errors.New("it changed since it was read: show it again and repeat the change")

// RefusalFields is every field the identity service's error envelope
// may carry, decoded unconditionally by Refusal() and embedded in EVERY
// refusal type below. Repeat finding 1361 (earlier: 1334): APIError,
// StepUpRequiredError and StaleError used to each declare their OWN
// narrower subset of these fields, so a field a given type's struct (or
// its Error() method) omitted was lost for every refusal of that type,
// not only the one case a prior probe happened to exercise. One shared
// set of fields, decoded once and embedded everywhere, means a refusal
// kind can no longer have a narrower contract than any other.
type RefusalFields struct {
	Code        string
	Reason      string
	Detail      string
	Action      string
	Scope       string
	Requirement string
	DecisionID  string
	Message     string
	// Policies is passed through verbatim: the identity service's own
	// shape for this field is not otherwise pinned down, and RawMessage
	// means a caller that marshals it (the --json error envelope) gets
	// it unchanged rather than this package guessing a structure.
	Policies json.RawMessage
}

// describe renders EVERY field this refusal carries, in a fixed order,
// as "(name: value)" clauses. This is the ONLY place that decides which
// producer fields appear in a refusal's TEXT, and every Error() method
// below calls it for ALL of the producer's own data and nothing else of
// its own composing -- never a hand-picked subset, never a field printed
// by one type's Error() and not another's. Findings 1334, 1361, 1382 and
// 1385 were each a different field (Action/Scope; then
// Requirement/DecisionID/Message/Policies; then Policies again; then
// Reason and Policies again) missing from exactly one refusal kind's
// partial, hand-curated rendering. Each Error() method's own text is
// limited to the one fact that makes its KIND identifiable (the status
// code, the step-up id, the "changed since read" sentence); every other
// fact the producer supplied comes only from this function, so there is
// no second place left where a field could go missing for one kind and
// not another. Findings 1412/1415 (criterion 6 F3): printing Reason
// unconditionally (F2) without keeping the original Message-equals-
// Reason de-duplication meant the shared value printed twice; the
// de-duplication is restored below, in this one place, so it applies
// uniformly rather than needing to be remembered per kind again.
func (f RefusalFields) describe() string {
	var b strings.Builder
	add := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(&b, " (%s: %s)", label, value)
	}
	add("code", f.Code)
	add("reason", f.Reason)
	add("detail", f.Detail)
	add("action", f.Action)
	add("scope", f.Scope)
	add("needs", f.Requirement)
	add("decision", f.DecisionID)
	// Finding 1412/1415 (criterion 6 F3): Message is Roid's own echo of
	// Reason often enough that printing both prints the same value
	// twice. The de-duplication is only correct once Reason itself is
	// always printed (F2, above) -- it is, now -- so skip Message only
	// when it exactly repeats Reason; a Message that differs still
	// prints.
	if f.Message != f.Reason {
		add("message", f.Message)
	}
	if len(f.Policies) > 0 {
		add("policies", string(f.Policies))
	}
	return b.String()
}

// StaleError wraps ErrStale (errors.Is(err, ErrStale) keeps working)
// while preserving every applicable producer field for a 412, instead of
// discarding them for the bare sentinel.
type StaleError struct {
	RefusalFields
}

func (e *StaleError) Error() string {
	return ErrStale.Error() + e.describe()
}

func (e *StaleError) Unwrap() error { return ErrStale }

// StepUpRequiredError means the change hands out an escalation-class
// action and needs a security-key confirmation. Complete step-up ID, then
// repeat the same change with it.
type StepUpRequiredError struct {
	ID string
	// URL is the identity service's page where a browser completes the
	// step-up; empty from a service that names none.
	URL string
	RefusalFields
}

func (e *StepUpRequiredError) Error() string {
	return fmt.Sprintf("this change needs a security-key confirmation (step-up %s)", e.ID) + e.describe()
}

// APIError is any other refusal: the status and every applicable
// producer field (finding 1334/1361/1382/1385: a delegation-escalation,
// policy denial, fresh-sign-in challenge or blocking-policy refusal
// names the requirement, action, scope, decision, message and policy
// IDs it refused, and a renderer that drops them leaves a caller with
// only a status code for a decision the producer explained in full).
type APIError struct {
	Status int
	RefusalFields
}

func (e *APIError) Error() string {
	return fmt.Sprintf("the identity service refused the request (status %d)", e.Status) + e.describe()
}

// refusal turns a non-2xx result into one of the errors above. It returns
// nil for a success.
func Refusal(r *api.Result) error {
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return nil
	}
	var body struct {
		Error struct {
			Code        string          `json:"code"`
			Reason      string          `json:"reason"`
			Detail      string          `json:"detail"`
			Action      string          `json:"action"`
			Scope       string          `json:"scope"`
			Requirement string          `json:"requirement"`
			DecisionID  string          `json:"decision_id"`
			Message     string          `json:"message"`
			Policies    json.RawMessage `json:"policies"`
			StepUpID    string          `json:"step_up_id"`
			StepUpURL   string          `json:"step_up_url"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	e := body.Error
	fields := RefusalFields{
		Code: e.Code, Reason: e.Reason, Detail: e.Detail, Action: e.Action,
		Scope: e.Scope, Requirement: e.Requirement, DecisionID: e.DecisionID,
		Message: e.Message, Policies: e.Policies,
	}
	switch {
	case r.StatusCode == http.StatusPreconditionFailed:
		return &StaleError{RefusalFields: fields}
	case r.StatusCode == http.StatusForbidden && e.Requirement == "step_up" && e.StepUpID != "":
		return &StepUpRequiredError{ID: e.StepUpID, URL: e.StepUpURL, RefusalFields: fields}
	}
	if fields.Code == "" {
		fields.Code = http.StatusText(r.StatusCode)
	}
	return &APIError{Status: r.StatusCode, RefusalFields: fields}
}
