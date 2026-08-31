//go:build linux

package vpn

import (
	"strings"
	"testing"
)

/*
The unit's ExecStart must be a command line that parses.

`--socket-group %s` with an empty group emitted the flag with nothing after it. systemd splits
ExecStart on whitespace, so argv ended at `--socket-group`, cobra answered "flag needs an argument",
and the daemon exited before running a line of its own code. Restart=on-failure looped forever while
`systemctl enable --now` reported success, because a Type=simple unit succeeds as soon as exec does:
`vpn install` printed "RunOS VPN service installed." over a service that never came up, with nothing
on stdout or stderr to say why. The self-heal cannot help, because the process dies in flag parsing.

Reachable on a machine with neither a `sudo` nor a `wheel` group, installed from a root shell.
openSUSE has neither, and `su -` is its usual admin flow.
*/
func TestTheUnitNeverEmitsAFlagWithNoValue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		group    string
		explicit bool
		wantArgs string
	}{
		{
			// THE DEFECT: no administrators group on the machine, so nothing to name.
			name: "no group at all", group: "", explicit: false,
			wantArgs: "/usr/local/bin/runos vpn daemon",
		},
		{
			name: "a derived group", group: "sudo", explicit: false,
			wantArgs: "/usr/local/bin/runos vpn daemon --socket-group sudo",
		},
		{
			// Recorded so the daemon's self-heal leaves a chosen group alone.
			name: "a group somebody named", group: "developers", explicit: true,
			wantArgs: "/usr/local/bin/runos vpn daemon --socket-group developers --socket-group-source explicit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unit := renderSystemdUnit("/usr/local/bin/runos", tc.group, tc.explicit)

			var execStart string
			for _, line := range strings.Split(unit, "\n") {
				if strings.HasPrefix(line, "ExecStart=") {
					execStart = strings.TrimPrefix(line, "ExecStart=")
				}
			}
			if execStart != tc.wantArgs {
				t.Errorf("ExecStart = %q, want %q", execStart, tc.wantArgs)
			}
			// The shape that broke it: a flag as the last word, with nothing following.
			if strings.HasSuffix(execStart, "--socket-group") {
				t.Error("ExecStart ends in a flag with no value; the daemon cannot start")
			}
		})
	}
}
