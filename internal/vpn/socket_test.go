package vpn

import (
	"os"
	"path/filepath"
	"testing"
)

// shortSocketPath returns a socket path under the system temp dir. macOS caps a unix socket path
// at 104 bytes, and t.TempDir() paths are long, so tests put the socket somewhere short and clean
// it up themselves.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(os.TempDir(), name)
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

// The socket is the whole CLI-to-daemon contract, so a round trip proves the wire shape: a
// request in, the daemon's response out, and a distinct NotRunningError when nothing is
// listening. A real Daemon needs root (it opens a tun), so this drives the listener with a
// hand-built Daemon whose Handle is exercised through Serve without ever bringing an engine up:
// OpIdentity and OpStatus touch no engine.
func TestSocketRoundTripReturnsTheDaemonResponse(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadState(dir) // generates a keypair, no tunnel
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{stateDir: dir, version: "test", state: state, platform: newPlatform(), pollInterval: PollInterval}

	sock := shortSocketPath(t, "runos-vpn-test.sock")
	listener, err := Serve(d, sock, "")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	client := NewClient(sock)
	resp, err := client.Call(Request{Op: OpIdentity})
	if err != nil {
		t.Fatalf("identity call: %v", err)
	}
	if resp.Identity == nil || resp.Identity.PublicKey != state.PublicKey || resp.Identity.Version != "test" {
		t.Errorf("identity = %+v, want public key %s", resp.Identity, state.PublicKey)
	}

	status, err := client.Call(Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("status call: %v", err)
	}
	if status.Status == nil || status.Status.Running {
		t.Errorf("status = %+v, want not running", status.Status)
	}

	// An unknown op is an error carried in the response, surfaced as a Go error.
	if _, err := client.Call(Request{Op: "bogus"}); err == nil {
		t.Error("an unknown op must be an error")
	}
}

func TestSocketClientReportsNotRunningWhenNothingListens(t *testing.T) {
	client := NewClient(shortSocketPath(t, "runos-vpn-absent.sock"))
	_, err := client.Call(Request{Op: OpStatus})
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := err.(*NotRunningError); !ok {
		t.Errorf("error is %T, want *NotRunningError", err)
	}
}
