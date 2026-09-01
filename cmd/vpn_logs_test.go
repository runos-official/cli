package cmd

import "testing"

/*
The noise filter is the whole value of `vpn logs`.

Measured on a real machine 2026-09-01: /var/log/runos-vpn.log was 582 KB and all but one line was
`MallocStackLogging` chatter. A person asked to send that file sends half a megabyte that cannot
answer the question, which is why nobody asks twice.
*/
func TestTheNoiseFilterKeepsWhatTheDaemonWrote(t *testing.T) {
	noise := []string{
		"runos(80973) MallocStackLogging: can't turn off malloc stack logging because it was not enabled.",
		"",
		"   ",
	}
	for _, line := range noise {
		if !isDaemonLogNoise(line) {
			t.Errorf("kept a line nobody can act on: %q", line)
		}
	}

	// Every one of these is a line the daemon writes, and each is the answer to a different
	// support question. Dropping any of them would defeat the command.
	daemon := []string{
		`2026/09/01 12:17:17 vpn: control socket /var/run/runos-vpn.sock is mode 0660, group "staff" (gid 20)`,
		"2026/09/01 12:17:17 vpn: tunnel up on utun0 for account acct1 device device-1, conductor https://api.example.com",
		"2026/09/01 12:17:17 vpn: poll FAILED, will keep retrying every 30s: lookup api.example.com: no such host",
		"2026/09/01 12:31:02 vpn: poll recovered after 28 failed attempt(s) over 13m45s (last error: lookup api.example.com: no such host)",
		"2026/09/01 13:02:11 vpn: session has lapsed: peers removed, sign in again to restore the tunnel",
		"2026/09/01 13:05:00 vpn: tunnel down on utun0",
	}
	for _, line := range daemon {
		if isDaemonLogNoise(line) {
			t.Errorf("dropped a line the daemon wrote, which is the only content worth keeping: %q", line)
		}
	}
}
