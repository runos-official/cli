package vpn

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

/*
The log has to answer a support question without becoming one.

Measured on a real machine 2026-09-01: /var/log/runos-vpn.log held 582 KB, of which the useful
content was ONE line. A boot-time DNS failure had kept the VPN down for fourteen minutes and wrote
nothing at all. These tests pin the two properties that make the difference: a repeated failure
does not flood the file, and the recovery is recorded rather than merely implied by silence.
*/

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(old)
		log.SetFlags(flags)
	})
	fn()
	return buf.String()
}

func TestARepeatedFailureIsLoggedOnce(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() {
		for i := 0; i < 50; i++ {
			logPollOutcome(errString("lookup api.example.com: no such host"))
		}
	})
	if n := strings.Count(out, "poll FAILED"); n != 1 {
		t.Fatalf("wrote %d failure lines for one unchanged condition; want 1. A network down for an hour must not write 120 lines and bury the line that matters.\n%s", n, out)
	}
	if !strings.Contains(out, "keep retrying") {
		t.Error("the failure line must say the daemon keeps retrying: the previous version gave up silently, and that is what the reader needs to know")
	}
}

func TestTheRecoveryIsLogged(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() {
		logPollOutcome(errString("lookup api.example.com: no such host"))
		logPollOutcome(errString("lookup api.example.com: no such host"))
		logPollOutcome(nil)
	})
	if !strings.Contains(out, "recovered") {
		t.Fatalf("no recovery line. 'it started working again at 12:31' is exactly what a support conversation needs, and silence cannot say it.\n%s", out)
	}
	if !strings.Contains(out, "2 failed attempt") {
		t.Errorf("the recovery must say how many attempts it cost:\n%s", out)
	}
}

func TestASuccessfulSteadyStateWritesNothing(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() {
		for i := 0; i < 100; i++ {
			logPollOutcome(nil)
		}
	})
	if out != "" {
		t.Fatalf("a poll that changes nothing must write nothing; the file is owned by launchd and the daemon cannot rotate it.\n%s", out)
	}
}

func TestAQueryStringIsNeverWritten(t *testing.T) {
	// A signed URL carries its credential in the query string. This is the one shape that could
	// put a secret into a file a user is about to email to support.
	got := redactURL("https://api.example.com/acct1/vpn/state?token=SUPERSECRETVALUE&sig=abc")
	if strings.Contains(got, "SUPERSECRETVALUE") || strings.Contains(got, "sig=") {
		t.Fatalf("a credential survived redaction: %q", got)
	}
	if !strings.HasPrefix(got, "https://api.example.com/acct1/vpn/state") {
		t.Errorf("redaction removed the part a reader needs: %q", got)
	}
	// A URL with no query string must be untouched, or every ordinary log line gains noise.
	plain := "https://api.example.com"
	if redactURL(plain) != plain {
		t.Errorf("a plain URL was altered: %q", redactURL(plain))
	}
}

type errString string

func (e errString) Error() string { return string(e) }
