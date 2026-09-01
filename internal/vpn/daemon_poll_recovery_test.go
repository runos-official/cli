package vpn

import (
	"testing"
	"time"
)

/*
A transient network failure at daemon start must not wedge the VPN forever.

THE DEFECT, measured on a real machine 2026-09-01. The daemon runs from a LaunchDaemon with
RunAtLoad, so at boot it races the network stack, and losing that race is the ORDINARY case rather
than an edge one. `startTunnelLocked` did this:

	d.engine = eng                        // utun0 created and UP
	d.client = newConductorClient(...)
	if err := d.pollAndApplyLocked(); err != nil {
	    return err                        // the DNS failure returned HERE
	}
	d.startPollLoop()                     // never reached

So the FIRST poll failing meant the poll loop was never started at all. `startPollLoop` has exactly
one caller, and it sits after that return. `Resume` recorded the error into lastApplyErr and gave
up. The daemon then sat with an interface up, a client set and a valid session, polling never,
while DNS recovered minutes later and nothing noticed.

What the operator saw: utun0 UP with no address and no routes; `lastPollAt` frozen at daemon start;
`lastPollError` naming a DNS failure that had long since cleared. The only recovery was a manual
`vpn up` or a daemon restart.

The tunnel is UP either way. The question these tests ask is whether the daemon keeps TRYING.
*/

// A daemon whose first poll will fail, with the poll loop wired to a short interval so a test can
// observe a retry without waiting 30 seconds.
func newRetryTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newTestDaemon(t)
	d.pollInterval = 10 * time.Millisecond
	return d
}

func TestPollLoopStartsEvenWhenTheFirstPollFails(t *testing.T) {
	d := newRetryTestDaemon(t)

	// 192.0.2.1 is RFC 5737 TEST-NET-1 and unroutable, so the first poll fails the way a boot-time
	// DNS failure does.
	d.client = newConductorClient("http://192.0.2.1:3025", "acct", "device-1", "session")

	d.mu.Lock()
	err := d.beginPollingLocked()
	d.mu.Unlock()
	t.Cleanup(func() {
		d.mu.Lock()
		if d.cancelPoll != nil {
			d.cancelPoll()
		}
		d.mu.Unlock()
	})

	if err == nil {
		t.Fatal("the first poll was expected to fail against an unroutable address")
	}
	// The error is still reported. The point is that reporting it must not also mean giving up.
	if d.cancelPoll == nil {
		t.Fatal("no poll loop after a failed first poll: a transient failure at start is permanent")
	}

	// It must actually TICK, not merely exist. Each tick fails against the unroutable address;
	// what matters is that the daemon keeps trying.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		polled := !d.lastPoll.IsZero()
		d.mu.Unlock()
		if polled {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the poll loop never took a poll, so a failed first attempt is never retried")
}
