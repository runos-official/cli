package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
)

/*
FPL26 D1: there is ONE sign-in and it belongs to the CLI. Connecting the VPN consumes it and never
creates it.

`vpn up` used to resolve the credential before it decided a sign-in was needed, so a signed-out
machine exited 1 with an empty stdout and the remedy on stderr. RunOS Desktop reads stdout, drives
its Sign In button with this exact command, and discards stderr, so the button was a dead end that
could never sign anybody in. Reported 2026-08-28.
*/
func TestConnectingWithoutASignInNamesTheCommandThatFixesIt(t *testing.T) {
	err := vpnSignedOutError(fmt.Errorf("wrapped: %w", auth.ErrNotAuthenticated))

	if err == nil {
		t.Fatal("a signed-out connect must be an error, not a silent no-op")
	}
	msg := err.Error()
	if !strings.Contains(msg, "runos login") {
		t.Errorf("must name the command that establishes a sign-in, got %q", msg)
	}
	if strings.Contains(msg, "runos vpn up, which signs you in") {
		t.Errorf("connecting must not claim to sign anyone in, got %q", msg)
	}
}

// Any OTHER credential failure is somebody else's error and must not be reworded into a sign-out.
func TestOtherCredentialFailuresPassThroughUntouched(t *testing.T) {
	original := errors.New("keychain is locked")

	if got := vpnSignedOutError(original); !errors.Is(got, original) {
		t.Fatalf("must pass the original error through, got %v", got)
	}
}

/*
FPL26 D2/D3: the confirm-it-is-you step cannot change the active account, and the tunnel never
outlives the identity that opened it.

The device id and the device KEY are both account-scoped. When the browser came back on a different
account, `vpn up` carried on with the previous account's device id, which conductor answers 404 for
because loadDevice is account-scoped. That is the "sign in twice" report of 2026-08-28.
*/
func TestSigningInAsADifferentAccountStopsRatherThanCarryingOn(t *testing.T) {
	msg := describeAccountChange("aaaaa", "bbbbb")

	if msg == "" {
		t.Fatal("a re-auth that landed on another account must stop the connect")
	}
	if !strings.Contains(msg, "bbbbb") || !strings.Contains(msg, "aaaaa") {
		t.Errorf("must name both accounts so the person can tell what happened, got %q", msg)
	}
	if !strings.Contains(msg, "runos vpn up") {
		t.Errorf("must name the command that connects the new account, got %q", msg)
	}
}

// The ordinary case. Same account back from the browser is the whole point of a confirmation.
func TestConfirmingTheSameAccountCarriesOn(t *testing.T) {
	if msg := describeAccountChange("aaaaa", "aaaaa"); msg != "" {
		t.Fatalf("the same account must carry on silently, got %q", msg)
	}
}
