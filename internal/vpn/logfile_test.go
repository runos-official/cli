package vpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
The daemon's own log file, which exists because two of the three platforms had no log at all.

Measured by reading the service definitions (FCR349): the launchd plist names StandardOutPath, and
the systemd unit and the Windows service name nothing. On Linux the output lands in the journal,
which an ordinary user cannot read; on Windows it is dropped. A file the daemon owns is readable by
the person who needs it, and because the daemon owns it, it can keep it bounded.
*/

func TestTheDaemonLogStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 500; i++ { // 50 KB into a 1 KB cap
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	total := int64(0)
	for _, p := range []string{path, path + ".1"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	if total > 2*1024+100 {
		t.Fatalf("log holds %d bytes for a 1024 byte cap: nothing bounded it", total)
	}
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Fatal("the live file is empty right after a write: rotation lost the newest lines")
	}
}

func TestTheDaemonLogIsReadableWithoutRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o004 == 0 {
		t.Errorf("mode %v is not world readable, so a stuck user could not produce their own log", info.Mode().Perm())
	}
}

func TestAnExistingLogIsAppendedNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("before restart\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := openBoundedLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("after restart\n"))
	w.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "before restart\nafter restart\n" {
		t.Errorf("a daemon restart must not erase what led up to it, got %q", data)
	}
}

func TestWireguardErrorLinesAreRateLimited(t *testing.T) {
	// wireguard-go retries a failing handshake every few seconds; at that rate one dead endpoint
	// writes thousands of identical lines a day.
	out := captureLog(t, func() {
		logf := rateLimitedLogf(func(format string, args ...any) { logEvent("wireguard: "+format, args...) })
		for i := 0; i < 200; i++ {
			logf("%v - Failed to send handshake initiation: %v", "peer(abcd)", "network is unreachable")
		}
	})
	if n := strings.Count(out, "Failed to send handshake initiation"); n > 2 {
		t.Fatalf("wrote %d identical engine lines, want the first only:\n%s", n, out)
	}
}
