package vpn

import (
	"errors"
	"os"
	"strings"
	"testing"
)

/*
The connect path has to explain itself, and it must never leak while doing it.

FCR349. A user who never got a tunnel up produced a log with nothing in it, indistinguishable from a
user who never ran the command. These tests pin the three properties the fix depends on: a step is
written as one line a machine can read back, a failing step is found again by whoever reads the
log, and no credential ever reaches the file.
*/

func TestAFailingConnectStepIsInTheLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root this would create a real tun interface")
	}
	resetPollLog()
	d := newTestDaemon(t)
	if r := d.Handle(Request{Op: OpIdentity, AccountID: "aaaaa"}); r.Error != "" {
		t.Fatal(r.Error)
	}

	out := captureLog(t, func() { d.Handle(upRequest("aaaaa")) })

	// Creating the interface needs root, so a normal user's daemon-less run fails exactly here.
	// Before the fix the log stayed empty at this point.
	if !strings.Contains(out, "step=interface status=failed") {
		t.Fatalf("an up that could not create the interface wrote no failing step line:\n%q", out)
	}
	if strings.Contains(out, "session\"") || strings.Contains(out, "sessionToken") {
		t.Errorf("the log carried the session token field:\n%s", out)
	}
}

func TestEveryConnectStepIsNamed(t *testing.T) {
	// A step the report cannot describe is a step an agent cannot reason about.
	for _, step := range ConnectSteps {
		if StepMeaning(step) == "" {
			t.Errorf("step %q has no description of what it was trying to do", step)
		}
	}
	if StepMeaning("no-such-step") != "" {
		t.Error("an unknown step must not invent a meaning")
	}
}

func TestRepeatedStepFailureIsWrittenOnce(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() {
		for i := 0; i < 40; i++ {
			stepOutcome("routes", errors.New("route: network is unreachable"), "ok")
		}
	})
	if n := strings.Count(out, "step=routes status=failed"); n != 1 {
		t.Fatalf("wrote %d lines for one unchanged failure, want 1 (the poll loop re-asserts routes every tick):\n%s", n, out)
	}
}

func TestASucceedingStepIsWrittenOnceAndARecoveryIsRecorded(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() {
		for i := 0; i < 40; i++ {
			stepOutcome("dns", nil, "mode=resolver-files")
		}
		stepOutcome("dns", errors.New("write resolver file: permission denied"), "")
		stepOutcome("dns", errors.New("write resolver file: permission denied"), "")
		stepOutcome("dns", nil, "mode=resolver-files")
	})
	if n := strings.Count(out, "step=dns status=ok"); n != 2 {
		t.Fatalf("want the first success and the recovery, got %d ok lines:\n%s", n, out)
	}
	if n := strings.Count(out, "step=dns status=failed"); n != 1 {
		t.Fatalf("want one failure line, got %d:\n%s", n, out)
	}
}

func TestStepLinesNeverCarrySecrets(t *testing.T) {
	// Built at run time so the repository holds no key-shaped literal.
	b64Key := strings.Repeat("A", 43) + "="
	hexKey := strings.Repeat("ab", 32)
	jwt := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	cases := map[string]string{
		"private key":  "privateKey: " + b64Key,
		"wg public":    "peer " + b64Key + " rejected",
		"hex key":      "set private_key=" + hexKey,
		"bearer":       "Authorization: Bearer abc.def.ghi",
		"jwt":          "token was " + jwt,
		"session":      `{"sessionToken":"s3cret-value"}`,
		"psk":          "presharedKey=hunter2",
		"signed url":   "GET https://storage.example.com/obj?X-Signature=zzz&X-Credential=yyy failed",
		"uapi private": "private_key=" + hexKey + "\npublic_key=" + hexKey,
	}
	for name, in := range cases {
		out := redactText(in)
		for _, leak := range []string{b64Key, hexKey, "abc.def.ghi", jwt, "s3cret-value", "hunter2", "zzz", "yyy"} {
			if strings.Contains(out, leak) {
				t.Errorf("%s: %q survived redaction in %q", name, leak, out)
			}
		}
	}
	// Ids identify a report and are not credentials, so redaction must leave them readable.
	keep := "account 0f3a9c21 device 7b1e44d0 conductor https://api.example.com"
	if got := redactText(keep); got != keep {
		t.Errorf("redaction damaged an id line: %q", got)
	}
}

func TestParseLastFailureFindsTheUnresolvedStep(t *testing.T) {
	lines := []string{
		"2026/10/01 10:00:00 vpn: step=interface status=failed create tun interface: operation not permitted",
		"2026/10/01 10:01:00 vpn: step=interface status=ok created utun4 mtu=1420",
		"2026/10/01 10:01:01 vpn: step=routes status=failed route add: network is unreachable",
		"2026/10/01 10:01:02 vpn: tunnel down on utun4",
	}
	got := LastUnresolvedFailure(lines)
	if got == nil || got.Step != "routes" {
		t.Fatalf("got %+v, want the routes failure (the interface failure was resolved)", got)
	}
	if !strings.Contains(got.Detail, "network is unreachable") {
		t.Errorf("detail = %q", got.Detail)
	}
	if LastUnresolvedFailure(lines[:2]) != nil {
		t.Error("a failure followed by success of the same step is not a current failure")
	}
	if LastUnresolvedFailure([]string{"nothing useful", ""}) != nil {
		t.Error("lines with no step must give no failure")
	}
}

func TestAPollFailureCountsAsAFailingStep(t *testing.T) {
	resetPollLog()
	out := captureLog(t, func() { logPollOutcome(errors.New("lookup api.example.com: no such host")) })
	got := LastUnresolvedFailure([]string{out})
	if got == nil || got.Step != "poll" {
		t.Fatalf("the poll failure line must be readable as a step, got %+v from %q", got, out)
	}
}

func TestAnOlderDaemonsPollLinesAreStillRead(t *testing.T) {
	// A daemon that has not been restarted since the update keeps writing the old sentence.
	failing := []string{
		"2026/09/01 12:17:17 vpn: poll FAILED, will keep retrying every 30s: lookup api.example.com: no such host",
	}
	got := LastUnresolvedFailure(failing)
	if got == nil || got.Step != "poll" || !strings.Contains(got.Detail, "no such host") {
		t.Fatalf("got %+v", got)
	}
	recovered := append(failing, "2026/09/01 12:31:02 vpn: poll recovered after 28 failed attempt(s) over 13m45s (last error: x)")
	if LastUnresolvedFailure(recovered) != nil {
		t.Error("a recovery line must clear the failure")
	}
}

func TestAStepWhoseDetailChangedIsWrittenAgain(t *testing.T) {
	// A connect adds a peer to a tunnel that was already up. Reporting "0 peer(s) loaded" once and
	// then nothing hides the one change the person made.
	resetPollLog()
	out := captureLog(t, func() {
		stepOutcome("wireguard-config", nil, "0 peer(s) loaded")
		stepOutcome("wireguard-config", nil, "0 peer(s) loaded")
		stepOutcome("wireguard-config", nil, "1 peer(s) loaded")
		stepOutcome("wireguard-config", nil, "1 peer(s) loaded")
	})
	if n := strings.Count(out, "step=wireguard-config status=ok"); n != 2 {
		t.Fatalf("want a line per distinct outcome (2), got %d:\n%s", n, out)
	}
}

func TestAHandshakeSuccessForOneClusterDoesNotClearAnotherClustersFailure(t *testing.T) {
	// Two clusters share one tunnel. Cluster B coming up says nothing about cluster A.
	lines := []string{
		"2026/10/01 10:00:00 vpn: step=handshake status=failed no handshake with cluster clusterA after 1m0s: the server did not answer",
		"2026/10/01 10:00:30 vpn: step=handshake status=ok first handshake with cluster clusterB after 2s",
	}
	got := LastUnresolvedFailure(lines)
	if got == nil || got.Step != "handshake" || !strings.Contains(got.Detail, "clusterA") {
		t.Fatalf("got %+v, want the unresolved clusterA handshake failure", got)
	}
	lines = append(lines, "2026/10/01 10:05:00 vpn: step=handshake status=ok first handshake with cluster clusterA after 4m0s")
	if got := LastUnresolvedFailure(lines); got != nil {
		t.Errorf("got %+v, want none: clusterA handshaked, so its failure is resolved", got)
	}
}
