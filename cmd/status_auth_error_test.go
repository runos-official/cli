package cmd

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
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
