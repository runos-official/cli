package vpn

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
		"2026/09/01 12:31:02 vpn: poll recovered after 28 failed attempt(s) over 13m45s (last error: lookup api.example.com: no such host)",
		"2026/09/01 13:02:11 vpn: session has lapsed: peers removed, sign in again to restore the tunnel",
		"2026/09/01 13:05:00 vpn: tunnel down on utun0",
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

func TestReadLogReadsTheRotatedGenerationBeforeTheLiveFile(t *testing.T) {
	// A one-time failure line is written once. After a rotation it lives in <path>.1, and a machine
	// that is still broken must not read as "none recorded".
	dir := t.TempDir()
	live := filepath.Join(dir, "daemon.log")
	failure := "2026/10/01 10:00:00 vpn: step=interface status=failed create tun interface: operation not permitted"
	if err := os.WriteFile(live+".1", []byte(failure+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("2026/10/01 11:00:00 vpn: step=socket status=ok listening\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{live}, 10)
	if got.Path != live || len(got.Lines) != 2 || got.Lines[0] != failure {
		t.Fatalf("got %+v, want the rotated line first and the live line second", got)
	}
	if f := LastUnresolvedFailure(got.Lines); f == nil || f.Step != "interface" {
		t.Errorf("the failure that moved to the rotated file was lost: %+v", f)
	}
}

func TestReadLogWorksWhenOnlyTheRotatedFileExists(t *testing.T) {
	live := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(live+".1", []byte("vpn: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{live}, 10)
	if got.Path != live || len(got.Lines) != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestReadLogKeepsOnlyTheNewestLinesOfAHugeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.log")
	var b strings.Builder
	for i := 0; i < 200000; i++ {
		fmt.Fprintf(&b, "vpn: line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{path}, 3)
	if strings.Join(got.Lines, "|") != "vpn: line 199997|vpn: line 199998|vpn: line 199999" {
		t.Errorf("lines = %v", got.Lines)
	}
	if got.Total != 200000 {
		t.Errorf("total = %d, want every line counted", got.Total)
	}
	// "all" is still bounded: a legacy file can be gigabytes.
	if all := ReadLog([]string{path}, 0); len(all.Lines) != maxLogLines || all.Lines[len(all.Lines)-1] != "vpn: line 199999" {
		t.Errorf("tail 0 returned %d lines, want the newest %d", len(all.Lines), maxLogLines)
	}
}

func TestReadLogDoesNotStopAtAVeryLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	long := "vpn: " + strings.Repeat("x", 3<<20)
	body := "vpn: before\n" + long + "\nvpn: after\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{path}, 0)
	if got.Err != nil {
		t.Errorf("a long line is not a read error: %v", got.Err)
	}
	if len(got.Lines) != 3 || got.Lines[2] != "vpn: after" {
		t.Fatalf("the scan ended at the long line, got %d lines", len(got.Lines))
	}
	if len(got.Lines[1]) > maxLogLineBytes+64 {
		t.Errorf("the long line was kept at %d bytes", len(got.Lines[1]))
	}
}

func TestReadLogReportsAFileItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("opening a directory fails at open on windows")
	}
	// A directory opens on unix and fails on read: the same shape as a file that goes bad mid-read.
	got := ReadLog([]string{t.TempDir()}, 10)
	if got.Err == nil {
		t.Errorf("an unreadable log must say so, got %+v", got)
	}
}

func TestReadLogSaysPlainlyWhenTheLogExistsButCannotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix user who is not root")
	}
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("vpn: x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	got := ReadLog([]string{path}, 10)
	if got.Path != path || !errors.Is(got.Err, fs.ErrPermission) {
		t.Fatalf("got %+v, want the path and a permission error, not an empty result", got)
	}
	problem := got.Problem()
	for _, want := range []string{path, "cannot read", "sudo"} {
		if !strings.Contains(problem, want) {
			t.Errorf("the explanation lacks %q: %s", want, problem)
		}
	}
	if (LogResult{}).Problem() != "" {
		t.Error("no error must give no problem text")
	}
}
