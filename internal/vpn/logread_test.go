package vpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		if !isLogNoise(line) {
			t.Errorf("kept a line nobody can act on: %q", line)
		}
	}
	daemon := []string{
		`2026/09/01 12:17:17 vpn: control socket /var/run/runos-vpn.sock is mode 0660, group "staff" (gid 20)`,
		"2026/09/01 12:17:17 vpn: tunnel up on utun0 for account acct1 device device-1, conductor https://api.example.com",
		"2026/09/01 12:17:17 vpn: step=poll status=failed poll FAILED, will keep retrying every 30s: lookup api.example.com: no such host",
		"2026/09/01 13:02:11 vpn: session has lapsed: peers removed, sign in again to restore the tunnel",
	}
	for _, line := range daemon {
		if isLogNoise(line) {
			t.Errorf("dropped a line the daemon wrote, which is the only content worth keeping: %q", line)
		}
	}
}

func TestReadLogUsesTheFirstFileThatExistsAndSaysWhich(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.log")
	body := "MallocStackLogging: noise\n" + "vpn: one\n" + "vpn: two\n" + "vpn: three\n"
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{filepath.Join(dir, "absent.log"), legacy}, 2)
	if got.Path != legacy {
		t.Errorf("path = %q, want the legacy file", got.Path)
	}
	if strings.Join(got.Lines, "|") != "vpn: two|vpn: three" {
		t.Errorf("lines = %v, want the last two", got.Lines)
	}
	if got.Total != 3 || got.Skipped != 1 {
		t.Errorf("total=%d skipped=%d, want 3 and 1", got.Total, got.Skipped)
	}
}

func TestReadLogRedactsEvenWhatTheDaemonWrote(t *testing.T) {
	// An older daemon, or wireguard-go itself, may have written something the new rules would not.
	path := filepath.Join(t.TempDir(), "d.log")
	secret := strings.Repeat("B", 43) + "="
	if err := os.WriteFile(path, []byte("vpn: weird privateKey: "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{path}, 0)
	if strings.Contains(strings.Join(got.Lines, "\n"), secret) {
		t.Error("a key survived into the report")
	}
}

func TestReadLogMissingEverywhereReportsNoFile(t *testing.T) {
	got := ReadLog([]string{filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")}, 10)
	if got.Path != "" || len(got.Lines) != 0 || got.Err != nil {
		t.Errorf("got %+v, want an empty result with no error", got)
	}
}
