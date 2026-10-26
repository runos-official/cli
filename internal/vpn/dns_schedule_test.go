package vpn

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeDNSClock is a clock the loop's sleep advances, so minutes of schedule take no time.
type fakeDNSClock struct {
	t      time.Time
	sleeps int
}

func newFakeDNSClock() *fakeDNSClock {
	return &fakeDNSClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeDNSClock) loop(s dnsSchedule) dnsWatchLoop {
	return dnsWatchLoop{
		schedule: s,
		now:      func() time.Time { return c.t },
		sleep: func(ctx context.Context, d time.Duration) bool {
			c.sleeps++
			if ctx.Err() != nil {
				return false
			}
			c.t = c.t.Add(d)
			return true
		},
	}
}

// lookScript answers each look from a function of the time since the watch began, and records the
// looks, so a test sees how many looks re-published.
type lookScript struct {
	clock      *fakeDNSClock
	start      time.Time
	status     func(elapsed time.Duration) (DNSStatus, error)
	looks      int
	republish  int
	republishT []time.Duration
}

func (s *lookScript) look(republish bool) (DNSStatus, error) {
	s.looks++
	elapsed := s.clock.t.Sub(s.start)
	if republish {
		s.republish++
		s.republishT = append(s.republishT, elapsed)
	}
	return s.status(elapsed)
}

var (
	pendingStatus = DNSStatus{Mode: "native", Pending: true, Error: dnsPendingText}
	readyStatus   = DNSStatus{Available: true, Mode: "native"}
)

// appearsAt reports ready once the clock is at or past d; a negative d never does.
func appearsAt(d time.Duration) func(time.Duration) (DNSStatus, error) {
	return func(elapsed time.Duration) (DNSStatus, error) {
		if d >= 0 && elapsed >= d {
			return readyStatus, nil
		}
		return pendingStatus, nil
	}
}

func TestDNSWatchLoopEndings(t *testing.T) {
	boom := errors.New("another resolver answers")
	for _, tc := range []struct {
		name         string
		status       func(time.Duration) (DNSStatus, error)
		wantEnd      dnsEnd
		wantErr      error
		minElapsed   time.Duration
		maxElapsed   time.Duration
		minRepublish int
		maxRepublish int
	}{
		{"ready on the first look", appearsAt(0), dnsEndReady, nil, 0, time.Second, 0, 0},
		{"appears after the old five second wait", appearsAt(8 * time.Second), dnsEndReady, nil, 8 * time.Second, 9 * time.Second, 1, 1},
		{"appears after the 20 second wait", appearsAt(40 * time.Second), dnsEndReady, nil, 40 * time.Second, 46 * time.Second, 2, 3},
		{"never appears: gives up at the bound", appearsAt(-1), dnsEndGaveUp, nil, 180 * time.Second, 186 * time.Second, 10, 14},
		{"republishes more than once", appearsAt(100 * time.Second), dnsEndReady, nil, 100 * time.Second, 106 * time.Second, 5, 8},
		{"another resolver is a fault at once", func(time.Duration) (DNSStatus, error) {
			return DNSStatus{Mode: "unavailable"}, boom
		}, dnsEndFault, boom, 0, time.Second, 0, 0},
		{"no zone wanted is idle", func(time.Duration) (DNSStatus, error) {
			return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
		}, dnsEndIdle, nil, 0, time.Second, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeDNSClock()
			script := &lookScript{clock: c, start: c.t, status: tc.status}

			end, _, err := c.loop(defaultDNSSchedule).run(context.Background(), script.look)

			if end != tc.wantEnd {
				t.Fatalf("end = %v, want %v", end, tc.wantEnd)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if got := c.t.Sub(script.start); got < tc.minElapsed || got > tc.maxElapsed {
				t.Errorf("elapsed = %v, want between %v and %v", got, tc.minElapsed, tc.maxElapsed)
			}
			if script.republish < tc.minRepublish || script.republish > tc.maxRepublish {
				t.Errorf("republishes = %d at %v, want %d to %d", script.republish, script.republishT, tc.minRepublish, tc.maxRepublish)
			}
		})
	}
}

// A republish is never asked for twice within republishEvery, and the first one waits for
// republishFirst: the file is not rewritten while macOS may still be reading it.
func TestDNSWatchLoopSpacesRepublishes(t *testing.T) {
	c := newFakeDNSClock()
	script := &lookScript{clock: c, start: c.t, status: appearsAt(-1)}
	c.loop(defaultDNSSchedule).run(context.Background(), script.look)

	s := defaultDNSSchedule
	if len(script.republishT) < 3 || script.republishT[0] < s.republishFirst {
		t.Fatalf("republishes at %v, want the first no earlier than %v", script.republishT, s.republishFirst)
	}
	for i := 1; i < len(script.republishT); i++ {
		if gap := script.republishT[i] - script.republishT[i-1]; gap < s.republishEvery {
			t.Errorf("republishes %d and %d are %v apart, want at least %v", i-1, i, gap, s.republishEvery)
		}
	}
}

func TestDNSWatchLoopStopsWhenCancelled(t *testing.T) {
	c := newFakeDNSClock()
	ctx, cancel := context.WithCancel(context.Background())
	looks := 0
	end, _, err := c.loop(defaultDNSSchedule).run(ctx, func(bool) (DNSStatus, error) {
		looks++
		if looks == 2 {
			cancel()
		}
		return pendingStatus, nil
	})
	if end != dnsEndCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("end = %v, err = %v, want cancelled", end, err)
	}
	if looks != 2 {
		t.Errorf("looks = %d, want the loop to stop at the cancel", looks)
	}
}

func TestDNSScheduleGapAndRepublishDue(t *testing.T) {
	s := defaultDNSSchedule
	for _, tc := range []struct {
		elapsed time.Duration
		want    time.Duration
	}{
		{0, s.quickPoll}, {9 * time.Second, s.quickPoll}, {10 * time.Second, s.slowPoll}, {170 * time.Second, s.slowPoll},
	} {
		if got := s.gap(tc.elapsed); got != tc.want {
			t.Errorf("gap(%v) = %v, want %v", tc.elapsed, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name          string
		elapsed, last time.Duration
		want          bool
	}{
		{"too early for the first", 2 * time.Second, -1, false},
		{"first is due", 3 * time.Second, -1, true},
		{"too soon after one", 10 * time.Second, 3 * time.Second, false},
		{"next is due", 18 * time.Second, 3 * time.Second, true},
	} {
		if got := s.republishDue(tc.elapsed, tc.last); got != tc.want {
			t.Errorf("%s: republishDue = %v, want %v", tc.name, got, tc.want)
		}
	}
}
