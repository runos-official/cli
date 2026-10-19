package roidapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/runos-official/cli/internal/api"
)

// A step-up the CLI starts is completed in a browser, on the identity
// service's own page (step_up_url). The CLI shows the page, polls until the
// person has confirmed, then repeats the refused change once with the
// step-up. The identity service binds the step-up to that exact change and
// to this sign-in, and spends it on the retry.

// ErrStepUpExpired means the confirmation was not finished in time, or was
// already used. Run the command again for a new one.
var ErrStepUpExpired = errors.New("the security-key confirmation expired or was already used: run the command again")

// ErrStepUpTimeout means the CLI stopped waiting for the confirmation.
var ErrStepUpTimeout = errors.New("no security-key confirmation arrived in time: run the command again")

// StepUpWait is how the CLI waits for a browser confirmation. Sleep is
// time.Sleep outside tests.
type StepUpWait struct {
	Interval time.Duration
	Timeout  time.Duration
	Sleep    func(time.Duration)
}

// DefaultStepUpWait polls every two seconds for up to five minutes, the
// lifetime of a step-up.
func DefaultStepUpWait() StepUpWait {
	return StepUpWait{Interval: 2 * time.Second, Timeout: 5 * time.Minute, Sleep: time.Sleep}
}

// StepUpState returns pending, proven, used or expired for one of this
// sign-in's own step-ups.
func (c *Client) StepUpState(id string) (string, error) {
	tok, err := c.token()
	if err != nil {
		return "", err
	}
	res, err := c.http.Send(api.Request{Method: http.MethodGet, Path: "/cli/stepup/" + url.PathEscape(id), Token: tok})
	if err != nil {
		return "", err
	}
	if err := refusal(res); err != nil {
		return "", err
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil || body.State == "" {
		return "", fmt.Errorf("the identity service gave no step-up state")
	}
	return body.State, nil
}

// WaitForStepUp polls until the step-up is proven. It returns
// ErrStepUpExpired for an expired or used one and ErrStepUpTimeout when
// the wait runs out.
func (c *Client) WaitForStepUp(id string, w StepUpWait) error {
	if w.Interval <= 0 {
		w.Interval = 2 * time.Second
	}
	if w.Sleep == nil {
		w.Sleep = time.Sleep
	}
	var waited time.Duration
	for {
		state, err := c.StepUpState(id)
		if err != nil {
			return err
		}
		switch state {
		case "proven":
			return nil
		case "expired", "used":
			return ErrStepUpExpired
		}
		if waited >= w.Timeout {
			return ErrStepUpTimeout
		}
		w.Sleep(w.Interval)
		waited += w.Interval
	}
}

// DoConfirmed sends a call. When the identity service asks for a step-up
// and names its page, show runs (the CLI prints the page and opens a
// browser), the CLI waits for the confirmation and repeats the call once
// with it. A call that already carries a step-up is sent as it is, and a
// refusal with no page comes back unchanged.
func (c *Client) DoConfirmed(call Call, show func(*StepUpRequiredError), w StepUpWait) (*api.Result, error) {
	res, err := c.Do(call)
	var su *StepUpRequiredError
	if err == nil || call.StepUp != "" || !errors.As(err, &su) || su.URL == "" {
		return res, err
	}
	show(su)
	if err := c.WaitForStepUp(su.ID, w); err != nil {
		return nil, err
	}
	call.StepUp = su.ID
	return c.Do(call)
}
