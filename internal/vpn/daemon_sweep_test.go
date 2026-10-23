package vpn

import (
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fakePlatform records what the daemon asks of the machine and changes nothing on it. The daemon
// tests must never use newPlatform(): on a developer machine that would write real resolver files.
type fakePlatform struct {
	mu        sync.Mutex
	teardowns []string
}

func (*fakePlatform) SetInterfaceAddress(string, netip.Prefix) error { return nil }
func (*fakePlatform) Routes(string) ([]netip.Prefix, error)          { return nil, nil }
func (*fakePlatform) AddRoute(string, netip.Prefix) error            { return nil }
func (*fakePlatform) RemoveRoute(string, netip.Prefix) error         { return nil }
func (*fakePlatform) ReconcileDNS(string, netip.Addr, []ResolverPlan) (DNSStatus, error) {
	return DNSStatus{Available: true, Mode: "native"}, nil
}

func (f *fakePlatform) Teardown(iface string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardowns = append(f.teardowns, iface)
	return nil
}

func (f *fakePlatform) teardownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.teardowns)
}

/*
A daemon that starts clears what a dead daemon left, whether or not it has a session to resume.

THE GAP. Split DNS is a set of files on the machine (/etc/resolver on macOS, NRPT rules on
Windows), written while a tunnel is up and removed by Teardown on a clean stop. A crash, a SIGKILL
after launchd's exit timeout, or a kill in the middle of a resume skips Teardown. The next daemon
only repaired them as a side effect of applying a document, so when it had no session to resume
(the session lapsed while the machine was off, or the person had signed out) the files stayed:
every lookup under those zones went to a resolver address that no interface carries any more.
*/
func TestDaemonStartClearsLeftoversWhenThereIsNoSessionToResume(t *testing.T) {
	d := newTestDaemon(t)
	fake := &fakePlatform{}
	d.platform = fake

	d.Resume()

	if fake.teardownCount() != 1 {
		t.Fatalf("teardown calls = %d, want 1: leftovers from a dead daemon stay on the machine", fake.teardownCount())
	}
}

func TestDaemonStartClearsLeftoversWhenTheStoredSessionHasLapsed(t *testing.T) {
	d := newTestDaemon(t)
	fake := &fakePlatform{}
	d.platform = fake
	identity, err := d.state.IdentityForAccount("acct")
	if err != nil {
		t.Fatal(err)
	}
	identity.SessionToken = "session"
	identity.SessionExpiresAt = time.Now().Add(-time.Hour)
	d.state.ActiveAccountID = "acct"

	d.Resume()

	if fake.teardownCount() != 1 {
		t.Fatalf("teardown calls = %d, want 1", fake.teardownCount())
	}
	if d.engine != nil {
		t.Fatal("a lapsed session must not bring a tunnel up")
	}
}
