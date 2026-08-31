package cmd

import (
	"net/http"
	"strings"
	"testing"
)

/*
MEASURED ON A LIVE MACHINE, 2026-08-31, and it is the same split-brain one layer up.

An operator pointed the CLI at a local conductor while holding Firebase credentials minted against
a different project. Conductor refused every request:

	$ runos clusters list
	Error: authentication refused (401): Invalid token

	$ runos status
	Authentication: ✓ Logged in

Both from the same credential, seconds apart. `runos status` asks Google whether the refresh token
is good, and it is; whether CONDUCTOR accepts the resulting token is a different question, and
`probeSession` was throwing the answer away. It reported a refusal only when the body carried
`auth.session_expired`, and treated every OTHER 401 as "nothing learned".

A 401 is not nothing learned. It is the credential being refused, which is exactly what the person
needs told, and it is the difference between "run runos login" and half an hour wondering why a
command fails while status says they are fine.

403 and 5xx stay unknown, deliberately. A 403 means the credential was ACCEPTED and the caller is
not allowed to do that one thing, and reporting it as a sign-out would send someone to a browser to
fix a permission.
*/
func TestARefusedCredentialIsNotAWorkingSignIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		accepted bool
		known    bool
	}{
		// One case, because the verdict does not depend on WHY. A session that aged out
		// (`auth.session_expired`) and a token refused outright are both 401 and both refusals;
		// the reason picks the sentence the caller prints, not whether the credential works.
		{name: "a refusal", status: http.StatusUnauthorized, accepted: false, known: true},
		{name: "forbidden is a permission, not a sign-out", status: http.StatusForbidden, known: false},
		{name: "a server fault says nothing either way", status: http.StatusInternalServerError, known: false},
		{name: "accepted", status: http.StatusOK, accepted: true, known: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted, known := sessionProbeVerdict(tc.status)
			if known != tc.known {
				t.Fatalf("known = %v, want %v", known, tc.known)
			}
			if known && accepted != tc.accepted {
				t.Errorf("accepted = %v, want %v", accepted, tc.accepted)
			}
		})
	}
}

/*
And what the person is TOLD when the credential is refused but the session did not age out.

"Your session has expired" would be a guess: the token may be for another project, another
environment, or simply wrong. What is certain is that conductor will not accept it and that signing
in again is the thing to try.
*/
func TestARefusedCredentialNamesTheRemedyAndTheEnvironment(t *testing.T) {
	message := refusedCredentialMessage("Invalid token", "http://localhost:3025")

	if !strings.Contains(message, "runos login") {
		t.Errorf("must name the remedy, got %q", message)
	}
	if !strings.Contains(message, "Invalid token") {
		t.Errorf("must keep what conductor actually said, got %q", message)
	}
	/*
	 MEASURED 2026-08-31, and it is the whole point of this sentence.

	 An operator WAS signed in, correctly, and had switched the CLI to another environment. Their
	 credential belonged to the one they came from. "RunOS refused this sign-in" invites the honest
	 reply "no it did not, I am signed in", which is exactly what happened. Naming the address says
	 WHICH RunOS is refusing, and therefore which one to sign in to.
	*/
	if !strings.Contains(message, "localhost:3025") {
		t.Errorf("must name the environment doing the refusing, got %q", message)
	}
	if !strings.Contains(message, "different environment") {
		t.Errorf("must raise the likeliest cause, got %q", message)
	}
}

// With no address to name, it still says the useful half rather than printing an empty one.
func TestARefusedCredentialSurvivesAnUnknownEnvironment(t *testing.T) {
	message := refusedCredentialMessage("Invalid token", "")

	if strings.Contains(message, "  ") || strings.HasPrefix(message, " ") {
		t.Errorf("must not leave a hole where the address goes, got %q", message)
	}
	if !strings.Contains(message, "runos login") {
		t.Errorf("must still name the remedy, got %q", message)
	}
}
