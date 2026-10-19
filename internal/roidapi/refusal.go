package roidapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/runos-official/cli/internal/api"
)

// ErrStale means the resource changed after the CLI read it, so the
// identity service refused the change (412). Read it again and retry.
var ErrStale = errors.New("it changed since it was read: show it again and repeat the change")

// StepUpRequiredError means the change hands out an escalation-class
// action and needs a security-key confirmation. Complete step-up ID, then
// repeat the same change with it.
type StepUpRequiredError struct {
	ID     string
	Action string
	// URL is the identity service's page where a browser completes the
	// step-up; empty from a service that names none.
	URL string
}

func (e *StepUpRequiredError) Error() string {
	return fmt.Sprintf("this change needs a security-key confirmation (step-up %s)", e.ID)
}

// APIError is any other refusal: the status and the identity service's
// machine-readable code, with its reason when it gives one.
type APIError struct {
	Status int
	Code   string
	Reason string
	Detail string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("the identity service refused the request (%d %s)", e.Status, e.Code)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// refusal turns a non-2xx result into one of the errors above. It returns
// nil for a success.
func refusal(r *api.Result) error {
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return nil
	}
	var body struct {
		Error struct {
			Code        string `json:"code"`
			Reason      string `json:"reason"`
			Detail      string `json:"detail"`
			Action      string `json:"action"`
			Requirement string `json:"requirement"`
			StepUpID    string `json:"step_up_id"`
			StepUpURL   string `json:"step_up_url"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &body)
	e := body.Error
	switch {
	case r.StatusCode == http.StatusPreconditionFailed:
		return ErrStale
	case r.StatusCode == http.StatusForbidden && e.Requirement == "step_up" && e.StepUpID != "":
		return &StepUpRequiredError{ID: e.StepUpID, Action: e.Action, URL: e.StepUpURL}
	}
	code := e.Code
	if code == "" {
		code = http.StatusText(r.StatusCode)
	}
	return &APIError{Status: r.StatusCode, Code: code, Reason: e.Reason, Detail: e.Detail}
}
