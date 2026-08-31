package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
)

/*
FCR160. A ten second timeout against Google's token endpoint is not a sign-out.

`runos status` had one bucket for "the refresh failed", so a slow network reported the person as not
authenticated and put the raw Go error, request URL and Firebase API key included, straight into the
RunOS Desktop menu bar. Conductor is never contacted on this path, which is the first thing the
report asked about.
*/
func TestATimeoutIsAReachabilityProblemNotASignOut(t *testing.T) {
	timeout := fmt.Errorf("request failed: %w", &url.Error{
		Op:  "Post",
		URL: "https://securetoken.googleapis.com/v1/token?key=SOME_API_KEY",
		Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
	})

	kind, message := classifyAuthError(timeout)

	if kind != authErrorNetwork {
		t.Fatalf("a transport failure must be %q, got %q", authErrorNetwork, kind)
	}
	if strings.Contains(message, "securetoken") || strings.Contains(message, "SOME_API_KEY") {
		t.Errorf("the sentence a person reads must carry no URL and no key, got %q", message)
	}
	if !strings.Contains(strings.ToLower(message), "reach") {
		t.Errorf("must say the service could not be reached, got %q", message)
	}
}

// A refusal from Google IS a sign-out, and the remedy is the browser.
func TestARefusedRefreshIsASignOut(t *testing.T) {
	kind, message := classifyAuthError(errors.New("token refresh failed: TOKEN_EXPIRED"))

	if kind != authErrorRejected {
		t.Fatalf("a refusal must be %q, got %q", authErrorRejected, kind)
	}
	if !strings.Contains(message, "runos login") {
		t.Errorf("must name the remedy, got %q", message)
	}
}

// A DNS failure is the other common shape of "the network is not there".
func TestADNSFailureIsAlsoAReachabilityProblem(t *testing.T) {
	dns := fmt.Errorf("request failed: %w", &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("no such host"),
	})

	if kind, _ := classifyAuthError(dns); kind != authErrorNetwork {
		t.Fatalf("a dial failure must be %q, got %q", authErrorNetwork, kind)
	}
}

/*
The diagnostic detail is kept, and the API key is not.

A person pastes `runos status --json` into a chat window when something is wrong. The URL is worth
keeping there; the key on the end of it is not, and it costs one substitution to leave out.
*/
func TestDiagnosticDetailKeepsTheURLAndDropsTheKey(t *testing.T) {
	detail := redactAPIKey(`Post "https://securetoken.googleapis.com/v1/token?key=SOME_API_KEY": timeout`)

	if strings.Contains(detail, "SOME_API_KEY") {
		t.Errorf("the key must not survive, got %q", detail)
	}
	if !strings.Contains(detail, "securetoken.googleapis.com") {
		t.Errorf("the host is the useful half and must survive, got %q", detail)
	}
}

/*
The kind now comes from what the auth package MEASURED, not from the error's text.

The first pass at FCR160 asked "is this a `*url.Error`". That is true for a dead link and false for
every form of interference that ANSWERS, so the commonest one of all, a wifi portal returning its
own page with a 200, still produced "Your session has ended". That is the sentence FCR160 was
raised about, surviving in the case it was written for.

`internal/auth` now tags every failure with the fact it measured. These cases pin the mapping from
that tag to the two things a caller needs: the KIND, which the desktop app branches on, and the
SENTENCE, which a person reads.
*/
func TestTheKindComesFromWhatTheAuthPackageMeasured(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		wantKind    string
		wantMessage []string // fragments that must appear
		notMessage  []string // fragments that must NOT
	}{
		{
			name:        "a wifi portal answering instead of Google",
			err:         fmt.Errorf("%w: %w: intercepted", auth.ErrNetworkUnreachable, auth.ErrInterceptedReply),
			wantKind:    authErrorNetwork,
			wantMessage: []string{"network", "unaffected"},
			// THE DEFECT: this is the sentence FCR160 was raised about.
			notMessage: []string{"session has ended", "runos login"},
		},
		{
			name:        "nothing answered at all",
			err:         fmt.Errorf("%w: dial tcp: i/o timeout", auth.ErrNetworkUnreachable),
			wantKind:    authErrorNetwork,
			wantMessage: []string{"unaffected"},
			notMessage:  []string{"session has ended"},
		},
		{
			name:        "Google refused the credential",
			err:         fmt.Errorf("%w: TOKEN_EXPIRED", auth.ErrCredentialRefused),
			wantKind:    authErrorRejected,
			wantMessage: []string{"runos login"},
		},
		{
			// Signing in again cannot fix a key Google will not take, so the sentence must not
			// send anybody to a browser for it.
			name:        "Google refused this machine's API key",
			err:         fmt.Errorf("%w: API key not valid", auth.ErrClientMisconfigured),
			wantKind:    authErrorRejected,
			wantMessage: []string{"runos config get"},
			notMessage:  []string{"session has ended"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, message := classifyAuthError(tc.err)

			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(message, want) {
					t.Errorf("message %q must contain %q", message, want)
				}
			}
			for _, unwanted := range tc.notMessage {
				if strings.Contains(message, unwanted) {
					t.Errorf("message %q must NOT contain %q", message, unwanted)
				}
			}
		})
	}
}
