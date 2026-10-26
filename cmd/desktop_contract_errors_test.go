package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/desktopcontract"
	"github.com/runos-official/cli/internal/vpn"
)

/*
What a failed command leaves behind, as RunOS Desktop reads it.

On a failure Desktop reads stderr (cobra writes "Error: " and the error text there) and shows it. It
parses stdout only for the sign-in events. Desktop takes the LAST line before the "Stuck? 'runos vpn
diagnose'" hint as the sentence for a person, so the hint must stay a separate paragraph that starts
with exactly those words. The exit code is 1: no error here carries its own ExitCode.
*/

func assertPlainErrorText(t *testing.T, name, text string) {
	t.Helper()
	for _, problem := range desktopcontract.PlainErrorProblems(text) {
		t.Errorf("%s: %s\n%q", name, problem, text)
	}
}

func TestDesktopContractSignInAndStatusSentencesAreFitForADesktopMenu(t *testing.T) {
	sentences := map[string]string{
		"not signed in":       vpnSignedOutError(auth.ErrNotAuthenticated).Error(),
		"non-interactive":     signInRequiredError(true).Error(),
		"account changed":     describeAccountChange("aaaaa", "bbbbb"),
		"session did not set": "the sign-in did not refresh the session window; try 'runos login' then 'runos vpn up' again",
	}
	for name, err := range map[string]error{
		"auth intercepted":   auth.ErrInterceptedReply,
		"auth unreachable":   auth.ErrNetworkUnreachable,
		"auth misconfigured": auth.ErrClientMisconfigured,
		"auth refused":       auth.ErrCredentialRefused,
	} {
		_, message := classifyAuthError(err)
		sentences[name] = message
	}
	for name, text := range sentences {
		assertPlainErrorText(t, name, text)
	}
}

// A failed `vpn up --json`: events (and nothing else) on stdout, the sentence plus the hint as the
// error, and the plain sentence is the part Desktop shows.
func TestDesktopContractFailedVPNUpSpeaksTheDesktopContract(t *testing.T) {
	failures := map[string]func(t *testing.T) upResult{
		"signed out": func(t *testing.T) upResult {
			conductor := newFakeConductor(t)
			fakeFirebase(t)
			writeConfig(t, conductor.server.URL, "aaaaa", false)
			return runUp(t, newFakeDaemon(t).path)
		},
		"fresh sign-in needed, unattended": func(t *testing.T) upResult {
			conductor := newFakeConductor(t)
			conductor.mintReplies = []mintReply{signInRequiredOnMint()}
			fakeFirebase(t)
			writeConfig(t, conductor.server.URL, "aaaaa", true)
			return runUp(t, newFakeDaemon(t).path, "--non-interactive")
		},
		"service not installed": func(t *testing.T) upResult {
			conductor := newFakeConductor(t)
			fakeFirebase(t)
			writeConfig(t, conductor.server.URL, "aaaaa", true)
			return runUp(t, shortTestSocket(t))
		},
	}
	for name, run := range failures {
		t.Run(name, func(t *testing.T) {
			result := run(t)
			if result.err == nil {
				t.Fatal("the command must fail")
			}
			for _, line := range strings.Split(strings.TrimSpace(result.stdout), "\n") {
				if line == "" {
					continue
				}
				desktopcontract.Check(t, "stdout line "+line, []byte(line), desktopcontract.SignInEvent)
			}
			var ec interface{ ExitCode() int }
			if errors.As(result.err, &ec) {
				t.Errorf("the error carries exit code %d; Desktop and scripts expect the plain failure code 1", ec.ExitCode())
			}
			text := result.err.Error()
			sentence, hint, found := strings.Cut(text, "\n\n"+stuckHint)
			if !found || hint != "" {
				t.Fatalf("the error must end with the diagnose hint as its own paragraph, got %q", text)
			}
			assertPlainErrorText(t, name, sentence)
			if !strings.HasPrefix(stuckHint, "Stuck? 'runos vpn diagnose'") {
				t.Errorf("Desktop skips the hint by its first words, got %q", stuckHint)
			}
		})
	}
}

// A signed-out `vpn up --json` also says so as an event, which is the only channel the sign-in
// window reads; the contract checks its shape and this checks the reason Desktop branches on.
func TestDesktopContractSignedOutVPNUpEmitsANotSignedInEvent(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", false)
	result := runUp(t, newFakeDaemon(t).path)
	var event struct{ Event, Reason, Message string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.stdout)), &event); err != nil {
		t.Fatalf("stdout is not one JSON event: %v\n%q", err, result.stdout)
	}
	if event.Event != "error" || event.Reason != "not_signed_in" {
		t.Errorf("event = %+v, want an error event with reason not_signed_in", event)
	}
	assertPlainErrorText(t, "event message", event.Message)
}

// Without the daemon, `vpn status` fails with the words Desktop looks for to offer the install.
func TestDesktopContractMissingServiceFailureCarriesTheWordsDesktopMatches(t *testing.T) {
	var runErr error
	out := captureStdout(t, func() {
		runErr = vpnStatusAgainst(t, shortTestSocket(t))
	})
	if runErr == nil {
		t.Fatal("status must fail when no daemon listens")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("a failure must leave stdout empty, got %q", out)
	}
	text := runErr.Error()
	if !strings.Contains(text, "runos-test") || !strings.Contains(strings.ToLower(text), "vpn install") {
		t.Errorf("Desktop recognises a missing service by the socket path and \"vpn install\", got %q", text)
	}
	assertPlainErrorText(t, "missing service", text)
	var notRunning *vpn.NotRunningError
	if !errors.As(runErr, &notRunning) {
		t.Errorf("want a *vpn.NotRunningError, got %T", runErr)
	}
}
