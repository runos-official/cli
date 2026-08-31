package cmd

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"
)

/*
Defects found by a ten-lens adversarial review of the whole day's work, 2026-08-31.

Each of these is a thing the day's own commits either introduced or left standing. They are
gathered in one file because they were found in one pass, and each carries the argument for why it
is wrong rather than only the assertion.
*/

/*
SIGNING IN TO A DIFFERENT ACCOUNT MUST TAKE THE PREVIOUS ACCOUNT'S TUNNEL DOWN.

FPL26 D3 says the tunnel never outlives the identity that opened it, and every other path that
changes identity got the teardown today: `runos logout`, `runos account switch`, and the
confirmation inside `runos vpn up` when it lands on another account. `runos login` did not.

So: a tunnel is up on aaaaa, the person runs `runos login` (or clicks Sign In, which is
`runos login --json`), and the browser lands on bbbbb. The config is rewritten to bbbbb and nothing
is said to the root daemon, which still holds aaaaa's session token, aaaaa's device key and aaaaa's
routes, and carries on routing that account's traffic under a superseded identity.

It is the same function, `interactiveLoginReporting`, that `runos vpn up` calls for its
confirmation, and that caller DOES tear down on an account change. The teardown was added to the
caller instead of to the function every caller shares.
*/
func TestSigningInToADifferentAccountTakesTheTunnelDown(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.signInAccount = "bbbbb"

	command := newLoginTestCommand(daemon.path)
	if err := runLogin(command, nil); err != nil {
		t.Fatalf("login: %v", err)
	}

	if got := readConfig(t)["account_id"]; got != "bbbbb" {
		t.Fatalf("the machine must end up on the account it signed in to, got %v", got)
	}
	assertHasCall(t, daemon.recorded(), "down")
}

// Re-authenticating the SAME account is not a change. People run `runos login` to refresh a
// sign-in, and dropping their tunnel for it would be a daily annoyance with nothing gained.
func TestReAuthenticatingTheSameAccountLeavesTheTunnelUp(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.signInAccount = "aaaaa"

	if err := runLogin(newLoginTestCommand(daemon.path), nil); err != nil {
		t.Fatalf("login: %v", err)
	}

	for _, call := range daemon.recorded() {
		if call == "down" {
			t.Fatalf("re-authenticating the same account must not touch the tunnel, got %v", daemon.recorded())
		}
	}
}

// A first sign-in on a machine has no previous account, so there is nothing to take down.
func TestAFirstSignInDoesNotTouchTheTunnel(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "", false)
	conductor.signInAccount = "bbbbb"

	if err := runLogin(newLoginTestCommand(daemon.path), nil); err != nil {
		t.Fatalf("login: %v", err)
	}

	if calls := daemon.recorded(); len(calls) != 0 {
		t.Errorf("nothing to take down, got %v", calls)
	}
}

func newLoginTestCommand(socketPath string) *cobra.Command {
	command := &cobra.Command{}
	command.Flags().String("api-key", "", "")
	command.Flags().Bool("no-browser", true, "")
	command.Flags().Bool("json", true, "")
	command.Flags().String("socket", socketPath, "")
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	return command
}

/*
THE RESTART NOTICE MUST BE MEASURED AGAINST THE BUILD NOW ON DISK.

`currentVersion` is read from `version.Version`, the constant compiled into the RUNNING process.
Installing an update replaces the binary on disk and never re-execs, so that variable still holds
the OLD version for the rest of the command.

The notice compared the daemon against it. On the ordinary machine, where daemon and CLI were in
step at 1.17.0, `runos update` installs 1.18.0, the loaded daemon is now genuinely behind the
binary on disk, and the comparison is 1.17.0 against 1.17.0. Nothing prints. That is exactly the
case the notice exists for, and it is silent on every real update.

d4647d1 removed the false positive on an already-current machine and put a false negative in its
place. The unit test drove the predicate with the versions hand-fed, so the wiring stayed untested
and the suite stayed green.
*/
func TestTheRestartNoticeNamesTheBuildNowInstalled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running string
		result  updateComponentResult
		want    string
	}{
		{
			// THE DEFECT: an update happened, so what is on disk is the version it installed.
			name:    "after an install, the installed version",
			running: "1.17.0",
			result:  updateComponentResult{Updated: true, Version: "1.18.0"},
			want:    "1.18.0",
		},
		{
			name:    "nothing installed, so what is running is what is on disk",
			running: "1.17.0",
			result:  updateComponentResult{Updated: false, Version: "1.17.0"},
			want:    "1.17.0",
		},
		{
			// A local build skips the CLI update entirely and reports no version to install.
			name:    "a dev build that skipped the update",
			running: "dev-2026-08-31T09:48:44Z",
			result:  updateComponentResult{Updated: false},
			want:    "dev-2026-08-31T09:48:44Z",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := installedCLIVersion(tc.running, tc.result); got != tc.want {
				t.Errorf("installedCLIVersion(%q, %+v) = %q, want %q", tc.running, tc.result, got, tc.want)
			}
		})
	}
}

// The composition, which is the half that was wrong: an update that lands on a machine whose
// daemon was already in step must now say so.
func TestAnUpdateOnAnInStepMachineAsksForARestart(t *testing.T) {
	installed := installedCLIVersion("1.17.0", updateComponentResult{Updated: true, Version: "1.18.0"})

	if !vpnDaemonNeedsRestart(true, "1.17.0", installed) {
		t.Fatal("the daemon is loaded on 1.17.0 and 1.18.0 is now on disk; that is the case the notice exists for")
	}
	notice := vpnRestartNotice("1.17.0", installed)
	if !strings.Contains(notice, "1.18.0") {
		t.Errorf("the notice must name the build now installed, got %q", notice)
	}
}

/*
A MISMATCH IS ONLY A MISMATCH WHILE A TUNNEL IS UP.

`account switch` now sends OpDown instead of connecting the new account. `handleDownLocked` clears
the session and stops the tunnel but leaves `ActiveAccountID` set, because only the forget path
clears it, and `statusLocked` reads the account from there. So the daemon keeps answering with the
OLD account, `Running: false`, and no session.

`runos status` compared that field against the config and tested neither. After
`runos vpn up` on aaaaa and `runos account switch bbbbb`, every `runos status` reported
"The VPN is still signed in to aaaaa, so the clusters 'runos vpn status' lists belong to aaaaa" for
a tunnel that is down and lists nothing. It persists until the next `runos vpn up`.
*/
func TestNoAccountMismatchIsClaimedForATunnelThatIsDown(t *testing.T) {
	if vpnAccountMismatch("bbbbb", "aaaaa", false) {
		t.Error("a tunnel that is down is not signed in to anything")
	}
	if !vpnAccountMismatch("bbbbb", "aaaaa", true) {
		t.Error("a tunnel that IS up on another account is the case this exists for")
	}
	if vpnAccountMismatch("aaaaa", "aaaaa", true) {
		t.Error("the same account is not a mismatch")
	}
	if vpnAccountMismatch("", "aaaaa", true) || vpnAccountMismatch("bbbbb", "", true) {
		t.Error("an unknown account on either side cannot be compared")
	}
}

/*
A RE-ENROL THAT NEEDS A SIGN-IN MUST ASK FOR ONE, NOT PANIC.

`enrolDevice` answers (nil, true, nil) when conductor refuses with `auth.session_expired`, and the
first call honours that. The re-enrol after a revoked key assigns to `device, _, err`, dropping the
flag, so `device` is nil and `err` is nil, the error guard passes, and the next line reads
`device.ID`.

`runos vpn up` then dies with a Go panic and a stack trace instead of asking for a sign-in, and the
menu-bar app parsing its stdout gets a panic on stderr and a signal exit rather than JSON. It needs
a revoked key AND conductor's session verdict to change between two calls one round trip apart,
which is narrow, but the failure is the worst kind available.
*/
func TestARevokedKeyWhoseReEnrolNeedsASignInAsksForOne(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", true)
	conductor.enrolReplies = []enrolReply{
		{status: http.StatusConflict, body: `{"error":"key revoked","code":"vpn.key_revoked"}`},
		{status: http.StatusUnauthorized, body: `{"error":"Your session has ended","code":"auth.session_expired"}`},
	}

	command := &cobra.Command{}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	// The assertion is that this RETURNS. Before the fix it dereferenced a nil device.
	device, session, err := prepareVPNSession(command, cfg, "a-token", vpn.NewClient(daemon.path))

	if err != nil {
		t.Fatalf("a session that needs a sign-in is not an error here: %v", err)
	}
	if session != nil || device != nil {
		t.Errorf("needing a sign-in returns nothing, got device=%v session=%v", device, session)
	}
}
