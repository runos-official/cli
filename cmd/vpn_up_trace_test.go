package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
FCR349: `vpn up` leaves a record of what it did, so a person whose connect failed can show where.

Sign-in, enrolment and the session mint run in the CLI, not the daemon, so the daemon log cannot
know about them. These drive the real command against the fakes and read the trace it leaves.
*/

func readUpTrace(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".runos", "vpn-up.log"))
	if err != nil {
		t.Fatalf("vpn up left no trace: %v", err)
	}
	return string(data)
}

func TestUpTraceNamesEveryStepOfASuccessfulConnect(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	if result := runUp(t, daemon.path); result.err != nil {
		t.Fatalf("up failed: %v", result.err)
	}

	trace := readUpTrace(t)
	for _, step := range []string{"credential", "key", "enrol", "session", "handoff"} {
		if !strings.Contains(trace, "step="+step+" status=ok") {
			t.Errorf("trace lacks a successful %q step:\n%s", step, trace)
		}
	}
	if strings.Contains(trace, "status=failed") {
		t.Errorf("a connect that worked recorded a failure:\n%s", trace)
	}
	// Nothing credential-shaped, whatever the fakes handed around.
	for _, leak := range []string{"REFRESH", "token", "Bearer"} {
		if strings.Contains(trace, leak) {
			t.Errorf("trace carries %q:\n%s", leak, trace)
		}
	}
}

func TestUpTraceNamesTheEnrolmentThatFailed(t *testing.T) {
	conductor := newFakeConductor(t)
	conductor.enrolReplies = []enrolReply{{status: 500, body: `{"message":"enrolment is down"}`}}
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	result := runUp(t, daemon.path)
	if result.err == nil {
		t.Fatal("an enrolment error must fail the connect")
	}

	trace := readUpTrace(t)
	if !strings.Contains(trace, "step=enrol status=failed") || !strings.Contains(trace, "enrolment is down") {
		t.Errorf("trace does not say which step failed and why:\n%s", trace)
	}
	if strings.Contains(trace, "step=session") {
		t.Errorf("the mint must not run, or be recorded, after a failed enrolment:\n%s", trace)
	}
}

func TestUpTraceNamesAnUnreachableService(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	result := runUp(t, filepath.Join(t.TempDir(), "absent.sock"))
	if result.err == nil {
		t.Fatal("no daemon must fail the connect")
	}
	trace := readUpTrace(t)
	if !strings.Contains(trace, "step=key status=failed") {
		t.Errorf("the first call to the service is the key step; the trace should say it failed:\n%s", trace)
	}
}

func TestUpTraceRecordsTheSignInRequest(t *testing.T) {
	conductor := newFakeConductor(t)
	conductor.mintReplies = []mintReply{signInRequiredOnMint(), {status: 200, body: `{"token":"T","expiresAt":"2099-01-01T00:00:00Z"}`}}
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	_ = runUp(t, daemon.path)

	trace := readUpTrace(t)
	if !strings.Contains(trace, "step=session status=warn") {
		t.Errorf("a mint that asked for a fresh sign-in should be a warn line, not silence:\n%s", trace)
	}
}
