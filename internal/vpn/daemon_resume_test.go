package vpn

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// stubEngine replaces the tun creation for a test and records how often the daemon asked.
func stubEngine(t *testing.T) *int {
	t.Helper()
	calls := 0
	previous := createEngine
	createEngine = func(string, bool) (tunnelEngine, error) {
		calls++
		return nil, errors.New("stub: no interface in tests")
	}
	t.Cleanup(func() { createEngine = previous })
	return &calls
}

func activeSession(t *testing.T, d *Daemon, expires time.Time) {
	t.Helper()
	identity, err := d.state.IdentityForAccount("acct")
	if err != nil {
		t.Fatal(err)
	}
	identity.SessionToken = "session"
	identity.SessionExpiresAt = expires
	identity.DeviceID = "device-1"
	identity.ConductorURL = "http://192.0.2.1:3025"
	d.state.ActiveAccountID = "acct"
}

/*
A daemon that restarts for an update brings the tunnel back from the stored session.

The restart after `runos update` stops the old daemon without ending the session (Close), and the
new one has to start the tunnel again with no sign-in. The connected clusters are not stored here:
Conductor holds them for the device, and the first poll returns them (BuildPlan turns that document
into peers, routes and DNS zones; see plan_test.go). So the one decision that belongs to the daemon
is this one, and it is pinned from both sides.
*/
func TestRestartedDaemonAsksForATunnelWhenTheStoredSessionIsLive(t *testing.T) {
	calls := stubEngine(t)
	d := newTestDaemon(t)
	d.platform = &fakePlatform{}
	activeSession(t, d, time.Now().Add(time.Hour))

	d.Resume()

	if *calls != 1 {
		t.Fatalf("tunnel requests = %d, want 1: a live stored session must come back without a sign-in", *calls)
	}
	if d.lastApplyErr == "" {
		t.Error("a failed resume must be recorded for status, not dropped")
	}
}

func TestRestartedDaemonAsksForNoTunnelWhenTheStoredSessionHasLapsed(t *testing.T) {
	calls := stubEngine(t)
	d := newTestDaemon(t)
	d.platform = &fakePlatform{}
	activeSession(t, d, time.Now().Add(-time.Minute))

	d.Resume()

	if *calls != 0 {
		t.Fatalf("tunnel requests = %d, want 0: a lapsed session needs a new sign-in", *calls)
	}
}

// The sweep must come BEFORE the tunnel: the platform teardown removes every split-DNS entry, so
// running it after the resume would delete the entries the new tunnel just wrote.
func TestStartUpSweepRunsBeforeTheTunnelIsRequested(t *testing.T) {
	d := newTestDaemon(t)
	fake := &fakePlatform{}
	d.platform = fake
	activeSession(t, d, time.Now().Add(time.Hour))
	var sweptWhenRequested int
	previous := createEngine
	createEngine = func(string, bool) (tunnelEngine, error) {
		sweptWhenRequested = fake.teardownCount()
		return nil, errors.New("stub")
	}
	t.Cleanup(func() { createEngine = previous })

	d.Resume()

	if sweptWhenRequested != 1 {
		t.Fatalf("teardowns before the tunnel was requested = %d, want 1", sweptWhenRequested)
	}
}

/*
A daemon that was stopped does not start a tunnel.

THE RACE. The resume runs on its own goroutine. When a stop signal wins the daemon lock first, Close
finds nothing to stop and returns, and the resume then ran and created a tunnel and split-DNS files
after the instance lock was released, with nobody left to remove them.
*/
func TestResumeAfterCloseDoesNotStartATunnel(t *testing.T) {
	calls := stubEngine(t)
	d := newTestDaemon(t)
	fake := &fakePlatform{}
	d.platform = fake
	activeSession(t, d, time.Now().Add(time.Hour))

	d.Close() // the stop wins the lock before the resume goroutine runs
	d.Resume()

	if *calls != 0 {
		t.Errorf("tunnel requests = %d, want 0: a closed daemon started a tunnel", *calls)
	}
	if fake.teardownCount() != 0 {
		t.Errorf("teardown calls = %d, want 0: a closed daemon touched the machine", fake.teardownCount())
	}
}

// A request that was already past the listener when the daemon closed cannot start a tunnel. The
// docs (edge case 6) rely on this.
func TestAnUpAfterCloseIsRefusedAndAsksForNoTunnel(t *testing.T) {
	calls := stubEngine(t)
	d := newTestDaemon(t)
	d.platform = &fakePlatform{}
	if _, err := d.state.IdentityForAccount("acct"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	resp := d.Handle(upRequest("acct"))

	if resp.Error == "" || !strings.Contains(resp.Error, "stopping") {
		t.Fatalf("error = %q, want a stopping refusal", resp.Error)
	}
	if *calls != 0 {
		t.Fatalf("tunnel requests = %d, want 0", *calls)
	}
}
