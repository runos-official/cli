package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"

	"github.com/spf13/cobra"
)

/*
`runos status` end to end, against a fake conductor and a fake Google.

WHY THIS IS SEPARATE FROM THE UNIT TESTS. `classifyAuthError`, `sessionProbeVerdict` and
`redactAPIKey` all have tables of their own, and every one of those tables still passed when the
CALL SITES were reverted to the old behaviour. Pure functions nobody calls are worth nothing, and
that gap is exactly how a defect survives a green suite. These drive the real command and read the
JSON a caller actually receives.
*/

// statusJSON runs the real `runos status --json` and parses what it printed. The command writes to
// os.Stdout directly, so stdout is swapped for a pipe rather than a cobra buffer.
func statusJSON(t *testing.T) map[string]any {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		cmd := &cobra.Command{Use: "status", RunE: runCLIStatus}
		cmd.Flags().BoolP("json", "j", false, "")
		cmd.Flags().String("socket", "", "")
		cmd.SetArgs([]string{"--json", "--socket", "/nonexistent/runos-test.sock"})
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("status failed: %v\n%s", runErr, out)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("status did not print JSON: %v\n%s", err, out)
	}
	return parsed
}

// profileConductor answers the account-profile probe with one scripted reply.
func profileConductor(t *testing.T, status int, body string, expiresAt string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expiresAt != "" {
			w.Header().Set("X-RunOS-Session-Expires-At", expiresAt)
		}
		respondJSON(w, status, body)
	}))
	t.Cleanup(server.Close)
	return server
}

/*
MEASURED ON A LIVE MACHINE, 2026-08-31.

An operator holding Firebase credentials from one project, pointed at a conductor that verifies
another, got this from two commands seconds apart:

	$ runos clusters list
	Error: authentication refused (401): Invalid token
	$ runos status
	Authentication: ✓ Logged in

The refresh against Google succeeds, so the old code called it a working sign-in. Whether CONDUCTOR
accepts the resulting token is a different question, and a 401 is the answer to it.
*/
func TestStatusDoesNotClaimASignInConductorRefuses(t *testing.T) {
	conductor := profileConductor(t, http.StatusUnauthorized, `{"error":"Invalid token"}`, "")
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authenticated"] != false {
		t.Errorf("authenticated = %v, want false: conductor refuses every request", status["authenticated"])
	}
	message, _ := status["authError"].(string)
	if !strings.Contains(message, "runos login") {
		t.Errorf("authError must name the remedy, got %q", message)
	}
	if !strings.Contains(message, "Invalid token") {
		t.Errorf("authError must keep what conductor said, got %q", message)
	}
	/*
	 AND IT MUST NOT CALL THIS AN EXPIRY.

	 The live case that produced this test was a credential minted against one Firebase project used
	 against a conductor that verifies another. Nothing about it had expired; it was refused. Both
	 send the person to `runos login`, which is exactly why the distinction is easy to lose, and a
	 flag named `sessionExpired` that fires for a token refused as invalid is a lie a client will
	 later act on.
	*/
	if _, present := status["sessionExpired"]; present {
		t.Errorf("a refused token is not an expired session, got sessionExpired=%v", status["sessionExpired"])
	}
}

// A session that genuinely aged out keeps its own flag, which is what RunOS Desktop branches on.
func TestStatusFlagsAnAgedOutSessionDistinctly(t *testing.T) {
	conductor := profileConductor(t, http.StatusUnauthorized,
		`{"error":"Your session has expired. Run `+"`runos login`"+` to sign in again.","code":"auth.session_expired"}`, "")
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authenticated"] != false || status["sessionExpired"] != true {
		t.Errorf("want authenticated=false and sessionExpired=true, got %v / %v",
			status["authenticated"], status["sessionExpired"])
	}
}

/*
A 403 is NOT a sign-out. The credential was accepted and one action was refused, and reporting that
as being signed out sends somebody to a browser to fix a permission.
*/
func TestStatusTreatsAForbiddenProbeAsSayingNothing(t *testing.T) {
	conductor := profileConductor(t, http.StatusForbidden, `{"error":"not allowed"}`, "")
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authenticated"] != true {
		t.Errorf("authenticated = %v, want true: a permission refusal is not a sign-out", status["authenticated"])
	}
	if _, present := status["authError"]; present {
		t.Errorf("a 403 must not produce an authError, got %v", status["authError"])
	}
}

// The happy path also carries WHEN the session ends, so a client can warn before it does.
func TestStatusReportsWhenTheSessionEnds(t *testing.T) {
	conductor := profileConductor(t, http.StatusOK, `{"companyName":"Example"}`, "2030-01-01T00:00:00Z")
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authenticated"] != true {
		t.Fatalf("authenticated = %v, want true", status["authenticated"])
	}
	if got, _ := status["sessionExpiresAt"].(string); !strings.HasPrefix(got, "2030-01-01") {
		t.Errorf("sessionExpiresAt = %q, want the header conductor stamped", got)
	}
}

/*
FCR160, wired up rather than unit tested.

The screenshot showed this in the RunOS Desktop menu bar:

	request failed: Post "https://securetoken.googleapis.com/v1/token?key=AIza...": context
	deadline exceeded (Client.Timeout exceeded while awaiting headers)

Conductor is not in that path and never was, which is the first thing the report asked. It is the
CLI's own refresh against Google, and it not completing says nothing about the sign-in.
*/
func TestStatusSeparatesAnUnreachableTokenServiceFromASignOut(t *testing.T) {
	conductor := profileConductor(t, http.StatusOK, `{}`, "")
	// A server that is closed immediately: every request fails in transport, like a dead link.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	auth.SetEndpointsForTest(t, dead.URL+"/signin", dead.URL+"/token")
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authErrorKind"] != authErrorNetwork {
		t.Fatalf("authErrorKind = %v, want %q", status["authErrorKind"], authErrorNetwork)
	}
	message, _ := status["authError"].(string)
	for _, leak := range []string{"http://", "https://", "key=", "context deadline"} {
		if strings.Contains(message, leak) {
			t.Errorf("the sentence a person reads must not contain %q, got %q", leak, message)
		}
	}
	if !strings.Contains(strings.ToLower(message), "reach") {
		t.Errorf("must say the service could not be reached, got %q", message)
	}
	// The detail is kept for diagnosis, with the key taken out.
	detail, _ := status["authErrorDetail"].(string)
	if detail == "" {
		t.Error("the diagnostic detail must survive; it is what a bug report is built from")
	}
	if strings.Contains(detail, "key=") && !strings.Contains(detail, "key=REDACTED") {
		t.Errorf("an API key must never survive into the detail, got %q", detail)
	}
}

// Google refusing the refresh IS a sign-out, and it gets the browser remedy.
func TestStatusReportsARefusedRefreshAsASignOut(t *testing.T) {
	conductor := profileConductor(t, http.StatusOK, `{}`, "")
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusBadRequest, `{"error":{"message":"TOKEN_EXPIRED"}}`)
	}))
	t.Cleanup(refusing.Close)
	auth.SetEndpointsForTest(t, refusing.URL+"/signin", refusing.URL+"/token")
	writeConfig(t, conductor.URL, "aaaaa", true)

	status := statusJSON(t)

	if status["authErrorKind"] != authErrorRejected {
		t.Fatalf("authErrorKind = %v, want %q", status["authErrorKind"], authErrorRejected)
	}
	if message, _ := status["authError"].(string); !strings.Contains(message, "runos login") {
		t.Errorf("must name the remedy, got %q", message)
	}
}

// A machine with no credentials at all says so plainly, and never reaches Google or conductor.
func TestStatusOnASignedOutMachineSaysSoWithoutAskingAnyone(t *testing.T) {
	var reached bool
	conductor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		respondJSON(w, http.StatusOK, `{}`)
	}))
	t.Cleanup(conductor.Close)
	fakeFirebase(t)
	writeConfig(t, conductor.URL, "aaaaa", false)

	status := statusJSON(t)

	if status["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", status["authenticated"])
	}
	if reached {
		t.Error("a machine with no credentials must not ask conductor anything")
	}
}
