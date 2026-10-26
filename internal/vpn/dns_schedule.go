package vpn

import (
	"context"
	"time"
)

/*
How long the daemon keeps checking that macOS applied the private DNS zones, and when it re-publishes.

Two measurements shape this (FCR 1730). On one run macOS listed the zone within a second. On another
it had not listed it five seconds later, and two minutes later it still had not, with the resolver
file on disk and the nameserver reachable through the tunnel. So the check cannot be a short wait
inside the connect: it runs in the background, bounded, and the connect does not depend on it.
*/

// dnsSchedule is the timing of the background check.
type dnsSchedule struct {
	// quickPoll is the gap between looks during the first quickWindow, when a republish is usually
	// done within seconds; slowPoll is the gap after that.
	quickPoll, slowPoll, quickWindow time.Duration
	// republishFirst is how long to wait before the first re-publish, republishEvery the gap after.
	republishFirst, republishEvery time.Duration
	// bound is when the daemon stops and reports that macOS never applied the zones.
	bound time.Duration
}

// defaultDNSSchedule: look twice a second for ten seconds, then every five; re-publish the file at
// three seconds and every fifteen after; give up at three minutes.
var defaultDNSSchedule = dnsSchedule{
	quickPoll: 500 * time.Millisecond, slowPoll: 5 * time.Second, quickWindow: 10 * time.Second,
	republishFirst: 3 * time.Second, republishEvery: 15 * time.Second,
	bound: 3 * time.Minute,
}

// gap is how long to wait before the next look, given the time since the first one.
func (s dnsSchedule) gap(elapsed time.Duration) time.Duration {
	if elapsed < s.quickWindow {
		return s.quickPoll
	}
	return s.slowPoll
}

// republishDue says whether this look should re-publish the resolver files. lastRepublish is the
// elapsed time of the previous re-publish, or a negative value when there was none.
func (s dnsSchedule) republishDue(elapsed, lastRepublish time.Duration) bool {
	if lastRepublish < 0 {
		return elapsed >= s.republishFirst
	}
	return elapsed-lastRepublish >= s.republishEvery
}

// dnsEnd says how a watch ended.
type dnsEnd int

const (
	// dnsEndReady: every zone is served by the tunnel resolver.
	dnsEndReady dnsEnd = iota
	// dnsEndFault: a look failed or another resolver overrides a zone. Waiting does not help.
	dnsEndFault
	// dnsEndGaveUp: the bound passed with the zones still unlisted.
	dnsEndGaveUp
	// dnsEndIdle: no zone is wanted any more, so nothing is left to wait for.
	dnsEndIdle
	// dnsEndCancelled: the tunnel stopped or the plan changed.
	dnsEndCancelled
)

// dnsWatchLoop runs the looks. The clock and the sleep are fields so a test runs minutes of schedule
// without waiting.
type dnsWatchLoop struct {
	schedule dnsSchedule
	now      func() time.Time
	// sleep waits d and reports false when ctx ended first.
	sleep func(ctx context.Context, d time.Duration) bool
}

// run calls look until it reports a final state. look is told whether this look should re-publish.
// It returns the last status look gave and, for dnsEndFault, the error.
func (l dnsWatchLoop) run(ctx context.Context, look func(republish bool) (DNSStatus, error)) (dnsEnd, DNSStatus, error) {
	start := l.now()
	lastRepublish := time.Duration(-1)
	var last DNSStatus
	for {
		elapsed := l.now().Sub(start)
		if elapsed >= l.schedule.bound {
			return dnsEndGaveUp, last, nil
		}
		if !l.sleep(ctx, l.schedule.gap(elapsed)) {
			return dnsEndCancelled, last, ctx.Err()
		}
		elapsed = l.now().Sub(start)
		republish := l.schedule.republishDue(elapsed, lastRepublish)
		if republish {
			lastRepublish = elapsed
		}
		status, err := look(republish)
		last = status
		switch {
		case ctx.Err() != nil:
			return dnsEndCancelled, last, ctx.Err()
		case err != nil:
			return dnsEndFault, last, err
		case status.Available:
			return dnsEndReady, last, nil
		case !status.Pending:
			return dnsEndIdle, last, nil
		}
	}
}
