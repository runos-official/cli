package cmd

import (
	"strings"
	"testing"
)

/*
The VPN-restart notice must fire on a REAL skew, not on every update.

REPORTED 2026-08-31. The operator ran `runos update` on an already-current machine and read this:

	Current CLI version: 1.18.0
	The CLI is already up to date.
	RunOS Desktop is already up to date.

	The VPN service is still running the PREVIOUS build: replacing the
	binary does not reload a daemon that is already loaded.
	  Pick it up with: sudo runos vpn restart

Nothing was replaced, so nothing could be running a previous build, and they were sent to type a
sudo command that would have done nothing. The condition was only "is the VPN service running",
which is true on every machine with a VPN, so the notice printed on every update whether or not one
happened.

The comparison to make it exact already existed and was already being used by `runos vpn status`:
the daemon reports its own build, so a skew is `daemon != cli` and nothing else. Their words for the
whole flow were "VERY janky", and this was the loudest part of it.
*/
func TestTheRestartNoticeFiresOnlyOnARealSkew(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		daemon  string
		cli     string
		want    bool
	}{
		{
			name:    "a daemon behind the CLI, which is the case the notice exists for",
			running: true, daemon: "1.17.0", cli: "1.18.0", want: true,
		},
		{
			// The reported bug: nothing updated, versions match, and it still spoke.
			name:    "a daemon already on this build says nothing",
			running: true, daemon: "1.18.0", cli: "1.18.0", want: false,
		},
		{
			name:    "no VPN service at all says nothing",
			running: false, daemon: "", cli: "1.18.0", want: false,
		},
		{
			// A daemon too old to report its version cannot be compared. Staying quiet is right:
			// the notice is advice, and advice given on no evidence is noise.
			name:    "a daemon that reports no version says nothing",
			running: true, daemon: "", cli: "1.18.0", want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := vpnDaemonNeedsRestart(tc.running, tc.daemon, tc.cli); got != tc.want {
				t.Errorf("vpnDaemonNeedsRestart(%v, %q, %q) = %v, want %v",
					tc.running, tc.daemon, tc.cli, got, tc.want)
			}
		})
	}
}

// The wording has to name the one command that fixes it, because the whole point of the notice is
// that `runos update` cannot do it: restarting needs admin and update does not.
func TestTheRestartNoticeNamesTheCommand(t *testing.T) {
	notice := vpnRestartNotice("1.17.0", "1.18.0")

	if !strings.Contains(notice, "sudo runos vpn restart") {
		t.Errorf("must name the command, got %q", notice)
	}
	// Both builds, so the reader can see the skew is real rather than take it on trust.
	if !strings.Contains(notice, "1.17.0") || !strings.Contains(notice, "1.18.0") {
		t.Errorf("must name both builds, got %q", notice)
	}
}
