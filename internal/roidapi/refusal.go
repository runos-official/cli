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
// may carry, decoded unconditionally by refusal() and embedded in EVERY
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

// fields renders every non-empty field as "(name: value)" clauses, in a
// fixed order, for a refusal type's Error() text. Shared so no future
// refusal type can omit a field its OWN Error() method forgets to print.
func (f RefusalFields) fields() string {
	var b strings.Builder
	write := func(label, value string) {
		if value == "" {
			return
		}
		b.WriteString(" (")
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteByte(')')
	}
	write("needs", f.Requirement)
	write("action", f.Action)
	write("scope", f.Scope)
	write("decision", f.DecisionID)
	if f.Detail != "" {
		write("detail", f.Detail)
	}
	if f.Message != "" && f.Message != f.Reason {
		write("message", f.Message)
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
	msg := ErrStale.Error()
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg + e.fields()
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
	msg := fmt.Sprintf("this change needs a security-key confirmation (step-up %s)", e.ID)
	if e.Code != "" {
		msg += " (" + e.Code + ")"
	}
	return msg + e.fields()
}

// APIError is any other refusal: the status and every applicable
// producer field (finding 1334/1361: a delegation-escalation, policy
// denial or fresh-sign-in challenge names the requirement, action,
// scope, decision and message it refused, and a renderer that drops
// them leaves a caller with only a status code for a decision the
// producer explained in full).
type APIError struct {
	Status int
	RefusalFields
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("the identity service refused the request (%d %s)", e.Status, e.Code)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg + e.fields()
}

// refusal turns a non-2xx result into one of the errors above. It returns
// nil for a success.
func refusal(r *api.Result) error {
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
