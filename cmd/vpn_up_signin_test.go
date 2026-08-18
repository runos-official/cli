package cmd

import (
	"strings"
	"testing"
)

/*
`vpn up` opens a browser when Conductor wants a fresh sign-in. That is right when a person typed
the command, and wrong when the desktop app runs it unattended at login: a browser window nobody
asked for, at the worst possible moment.

`--non-interactive` is how an unattended caller says "connect if you can, but never take over my
screen". It is not a quiet mode; the whole point is that it FAILS, clearly, so the caller can put
the sign-in in front of the person at a time of their choosing.
*/
func TestNonInteractiveUpRefusesRatherThanOpeningABrowser(t *testing.T) {
	err := signInRequiredError(true)

	if err == nil {
		t.Fatal("a sign-in that cannot be performed must be an error, not a silent no-op")
	}
	msg := err.Error()
	if !strings.Contains(msg, "runos vpn up") {
		t.Errorf("must name the command that completes it, got %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "sign-in") {
		t.Errorf("must say a sign-in is what is missing, got %q", msg)
	}
}

// Interactive is the default and must keep opening the browser: nil here means "carry on".
func TestInteractiveUpStillSignsIn(t *testing.T) {
	if err := signInRequiredError(false); err != nil {
		t.Fatalf("an interactive up must proceed to the browser, got %v", err)
	}
}
