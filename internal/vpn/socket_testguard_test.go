package vpn

import (
	"errors"
	"strings"
	"testing"
	"time"
)

/*
A test binary never dials the real daemon.

MEASURED twice on a developer machine: `go test ./...` took the real tunnel down and ended the real
VPN session. An account-change test passed no socket path, NewClient fell back to SocketPath, and the
root daemon behind it obeyed `down`. Every assertion on the path runs BEFORE any Call, so this test
cannot reach the real daemon even on code that still has the bug.
*/
func TestClientInATestBinaryNeverTargetsTheRealDaemon(t *testing.T) {
	for _, given := range []string{"", SocketPath} {
		c := NewClient(given)
		if c.socketPath == SocketPath {
			t.Fatalf("NewClient(%q) targets the real daemon socket %s in a test binary", given, SocketPath)
		}
		if strings.TrimSpace(c.socketPath) == "" {
			t.Fatalf("NewClient(%q) has no socket path", given)
		}
		_, err := c.CallWithin(Request{Op: OpStatus}, 2*time.Second)
		var notRunning *NotRunningError
		if !errors.As(err, &notRunning) {
			t.Fatalf("NewClient(%q).Call = %v, want the ordinary not-running error", given, err)
		}
	}
}

// An explicit path that is not the real one is the test's own fake and stays as given.
func TestClientKeepsAnExplicitTestSocket(t *testing.T) {
	path := shortSocketPath(t, "runos-vpn-explicit.sock")
	if got := NewClient(path).socketPath; got != path {
		t.Fatalf("NewClient(%q) dials %q", path, got)
	}
}

// Serve on the real path would remove the live daemon's socket file before it binds.
func TestServeRefusesTheRealSocketInATestBinary(t *testing.T) {
	listener, err := Serve(nil, SocketPath, "", false)
	if err == nil {
		listener.Close()
		t.Fatal("Serve bound the real daemon socket in a test binary")
	}
	if !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("Serve error does not say why: %v", err)
	}
}
