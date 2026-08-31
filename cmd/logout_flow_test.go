package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

/*
`runos logout` and the tunnel, which used to be unrelated.

MEASURED 2026-08-31 on a live machine. One invocation, both facts:

	runos status --json
	"authenticated": false
	"vpnRunning": true

Logging out cleared a JSON file. The VPN session lives in a root daemon that file cannot reach, so
the machine carried on routing traffic on an account it was no longer signed in to.

FPL26 D3: the tunnel never outlives the identity that opened it.
*/

func runLogoutCommand(t *testing.T, socketPath string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		cmd := &cobra.Command{Use: "logout", RunE: runLogout}
		cmd.Flags().String("socket", "", "")
		cmd.SetArgs([]string{"--socket", socketPath})
		err = cmd.Execute()
	})
	return out, err
}

func TestLogoutTakesTheTunnelDownWithTheIdentity(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	daemon.tunnelUp = true // the case this test is about: there IS a tunnel to take down
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	out, err := runLogoutCommand(t, daemon.path)
	if err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	/*
	 DOWN, NOT LOGOUT, and the difference is a device row.

	 The daemon's `logout` op forgets this machine's KEY as well as dropping the tunnel
	 (handleDownLocked(forget: true)). Enrolment is idempotent on the public key, so a forgotten key
	 means the next `vpn up` enrols a BRAND NEW device and the old row is left behind for ever.

	 MEASURED on a live account 2026-08-31: it already carries three rows for the same laptop, one
	 live and two revoked, from earlier key wipes. Making an ordinary sign-out do that would add one
	 more on every logout/login cycle, for no benefit: the key is useless without a session token,
	 and this has already ended the session.
	*/
	assertHasCall(t, daemon.recorded(), "down")
	assertNoCall(t, daemon.recorded(), "logout")
	if !strings.Contains(out, "Disconnected the VPN") {
		t.Errorf("a person must be told the tunnel went with it, got %q", out)
	}
	if strings.Contains(out, "key") {
		t.Errorf("no key was forgotten, so it must not say one was, got %q", out)
	}
	// And the credential is gone, which was the only thing it did before.
	cfg := readConfig(t)
	if cfg["refresh_token"] != nil && cfg["refresh_token"] != "" {
		t.Errorf("the credential must be cleared, got %v", cfg["refresh_token"])
	}
	if cfg["account_id"] != nil && cfg["account_id"] != "" {
		t.Errorf("the active account must be cleared, got %v", cfg["account_id"])
	}
}

/*
BEST EFFORT, and it has to stay that way.

Most machines have no VPN service installed. `runos desktop install` does not write the root
daemon, so a socket that is not there is the ORDINARY case, not a failure. Refusing to log somebody
out because a daemon they never installed did not answer would be a worse defect than the one this
fixes.
*/
func TestLogoutWorksOnAMachineWithNoVPNService(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	out, err := runLogoutCommand(t, "/nonexistent/there-is-no-daemon.sock")
	if err != nil {
		t.Fatalf("logout must work without a VPN service: %v", err)
	}

	if strings.Contains(out, "Disconnected the VPN") {
		t.Errorf("nothing was disconnected, so it must not claim so, got %q", out)
	}
	if cfg := readConfig(t); cfg["refresh_token"] != nil && cfg["refresh_token"] != "" {
		t.Error("the credential must still be cleared")
	}
}

// Logging out twice is not an error, and the second one has nothing to say.
func TestLogoutOnAnAlreadySignedOutMachineIsQuiet(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "", false)

	out, err := runLogoutCommand(t, daemon.path)
	if err != nil {
		t.Fatalf("logout failed: %v", err)
	}
	if !strings.Contains(out, "Already logged out") {
		t.Errorf("want the already-logged-out line, got %q", out)
	}
}

/*
SIGNING OUT WITH NO TUNNEL UP MUST NOT SAY IT DISCONNECTED ONE.

`down` answers with a status whether or not there was anything to take down, and the caller
branched on "did a status come back", which is true for every daemon that answers at all. The
daemon is a boot-start root service, so "installed, nothing connected" is the ordinary state
between sessions, and in it `down` ends no session and tears nothing down while `runos logout`
still printed "Disconnected the VPN."

The sign-out itself is unaffected. Only the sentence was wrong.
*/
func TestLogoutWithNoTunnelUpDoesNotClaimADisconnect(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t) // tunnelUp is false
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)

	out, err := runLogoutCommand(t, daemon.path)
	if err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	if strings.Contains(out, "Disconnected the VPN") {
		t.Errorf("nothing was connected, so nothing was disconnected, got %q", out)
	}
	if !strings.Contains(out, "Logged out successfully") {
		t.Errorf("the sign-out itself still has to happen and be reported, got %q", out)
	}
	// The teardown is still attempted: the daemon is the only thing that knows for sure.
	assertHasCall(t, daemon.recorded(), "down")
}
