package vpn

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// `vpn diagnose` is run by the person whose daemon is stuck, so a daemon that accepts a connection
// and never answers must cost the report seconds, not the two minutes a normal call may wait.
func TestProbeDaemonGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	sock := shortSocketPath(t, "runos-vpn-probe-hang.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // held open, never answered
		}
	}()
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = old }()

	start := time.Now()
	probe, status := ProbeDaemon(NewClient(sock))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the probe waited %s for a daemon that never answers", elapsed)
	}
	if probe.State != "error" || status != nil {
		t.Errorf("got %+v, %v; want an error state and no status", probe, status)
	}
}

// A service manager that hangs (a wedged systemd or launchd) must read as "unknown", not freeze the
// report.
func TestServiceQueryGivesUpOnAServiceManagerThatHangs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows service manager is queried through its API, not a command")
	}
	dir := t.TempDir()
	for _, tool := range []string{"systemctl", "launchctl"} {
		script := "#!/bin/sh\nexec sleep 30\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := serviceQueryTimeout
	serviceQueryTimeout = 300 * time.Millisecond
	defer func() { serviceQueryTimeout = old }()

	start := time.Now()
	running, err := NewService().Running()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the service query waited %s for a service manager that hangs", elapsed)
	}
	if running || err == nil {
		t.Errorf("running=%t err=%v; a query that timed out must be an error, not an answer", running, err)
	}
}
