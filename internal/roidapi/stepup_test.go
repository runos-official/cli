package roidapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func adminGrant() Call {
	return Call{Op: "createGrant", Params: map[string]string{"acct": "a1"},
		Body: map[string]any{"subject": map[string]string{"kind": "person", "id": "p_bob"}, "role": "admin", "scope": "acct/a1"}}
}

// noSleep makes a wait instant and counts the sleeps.
func noSleep(n *int) StepUpWait {
	return StepUpWait{Interval: time.Second, Timeout: time.Minute, Sleep: func(time.Duration) { *n++ }}
}

func TestAStepUpIsConfirmedInTheBrowserThenRetriedOnce(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	// The person finishes in the browser after the second poll.
	f.onPoll = func(id string, polls int) {
		if polls == 2 {
			f.stepUps[id] = true
		}
	}
	var shown []string
	sleeps := 0
	res, err := c.DoConfirmed(adminGrant(), func(su *StepUpRequiredError) { shown = append(shown, su.URL) }, noSleep(&sleeps))
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("confirmed change: %v %v", res, err)
	}
	if len(shown) != 1 || shown[0] != srv.URL+"/stepup/su_1" {
		t.Fatalf("the person was shown %v, want the step-up page once", shown)
	}
	last := f.headers[len(f.headers)-1]
	if last.Get("X-RunOS-Step-Up") != "su_1" {
		t.Fatalf("the retry did not carry the step-up")
	}
	posts := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST ") {
			posts++
		}
	}
	if posts != 2 || f.polls["su_1"] != 2 || sleeps < 1 {
		t.Fatalf("posts %d, polls %d, sleeps %d: want one refused change, one retry and polls until proven (%v)", posts, f.polls["su_1"], sleeps, f.calls)
	}
}

func TestAnExpiredStepUpStopsWithoutARetry(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	f.suState["su_1"] = "expired"
	sleeps := 0
	_, err := c.DoConfirmed(adminGrant(), func(*StepUpRequiredError) {}, noSleep(&sleeps))
	if !errors.Is(err, ErrStepUpExpired) {
		t.Fatalf("err = %v, want ErrStepUpExpired", err)
	}
	for _, call := range f.calls[1:] {
		if strings.HasPrefix(call, "POST ") {
			t.Fatalf("an expired step-up was retried: %v", f.calls)
		}
	}
}

func TestAStepUpWaitGivesUp(t *testing.T) {
	_, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	var waited time.Duration
	w := StepUpWait{Interval: 2 * time.Second, Timeout: 10 * time.Second, Sleep: func(d time.Duration) { waited += d }}
	_, err := c.DoConfirmed(adminGrant(), func(*StepUpRequiredError) {}, w)
	if !errors.Is(err, ErrStepUpTimeout) {
		t.Fatalf("err = %v, want ErrStepUpTimeout", err)
	}
	if waited < 10*time.Second || waited > 12*time.Second {
		t.Fatalf("waited %v, want about the 10 s timeout", waited)
	}
}

func TestAChangeWithAGivenStepUpIsNotPromptedAgain(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	f.stepUps["su_1"] = true
	call := adminGrant()
	call.StepUp = "su_1"
	prompted := false
	res, err := c.DoConfirmed(call, func(*StepUpRequiredError) { prompted = true }, StepUpWait{})
	if err != nil || res.StatusCode != 201 || prompted {
		t.Fatalf("given step-up: %v %v prompted=%v", res, err, prompted)
	}
}

func TestARefusalWithoutAStepUpPageIsReturnedAsIs(t *testing.T) {
	f, srv := newFakeRoid(t)
	c := New(srv.URL, staticToken("at_test"))
	f.baseURL = "" // an older roid that names no step-up page
	prompted := false
	_, err := c.DoConfirmed(adminGrant(), func(*StepUpRequiredError) { prompted = true }, StepUpWait{})
	var su *StepUpRequiredError
	if !errors.As(err, &su) || prompted || su.URL != "" {
		t.Fatalf("err = %v prompted=%v, want the plain step-up refusal", err, prompted)
	}
}
