package vpn

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// fakeResolverClock drives awaitEffectiveResolvers without real waiting. sleep advances the clock,
// and the zone becomes effective once the clock has moved appearAfter past the start.
type fakeResolverClock struct {
	start       time.Time
	now         time.Time
	appearAfter time.Duration // zero: effective at once; negative: never
	zone        string
	resolver    netip.Addr
	reads       int
	nudges      int
}

func newFakeResolverClock(appearAfter time.Duration) *fakeResolverClock {
	start := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	return &fakeResolverClock{
		start: start, now: start, appearAfter: appearAfter,
		zone: "zone.example.test", resolver: netip.MustParseAddr("192.0.2.53"),
	}
}

func (c *fakeResolverClock) wait() resolverWait {
	return resolverWait{
		read: func() (map[string][]netip.Addr, error) {
			c.reads++
			if c.appearAfter >= 0 && c.now.Sub(c.start) >= c.appearAfter {
				return map[string][]netip.Addr{c.zone: {c.resolver}}, nil
			}
			return map[string][]netip.Addr{}, nil
		},
		nudge:      func() { c.nudges++ },
		now:        func() time.Time { return c.now },
		sleep:      func(d time.Duration) { c.now = c.now.Add(d) },
		timeout:    20 * time.Second,
		poll:       250 * time.Millisecond,
		nudgeAfter: 3 * time.Second,
	}
}

func (c *fakeResolverClock) expected() []ResolverPlan {
	return []ResolverPlan{{Zone: c.zone, Resolver: c.resolver}}
}

func TestAwaitEffectiveResolversSucceedsAtOnceWithoutWaiting(t *testing.T) {
	c := newFakeResolverClock(0)
	if err := awaitEffectiveResolvers(c.wait(), c.expected()); err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if c.now != c.start || c.nudges != 0 {
		t.Errorf("waited %v with %d nudges, want neither", c.now.Sub(c.start), c.nudges)
	}
}

// The measured failure: a Mac that has just signed in needs more than the old five seconds. The
// wait must carry on past five seconds and nudge macOS exactly once, not give up.
func TestAwaitEffectiveResolversWaitsLongerThanFiveSeconds(t *testing.T) {
	c := newFakeResolverClock(12 * time.Second)
	if err := awaitEffectiveResolvers(c.wait(), c.expected()); err != nil {
		t.Fatalf("zone appeared after 12 s, want success, got %v", err)
	}
	if c.nudges != 1 {
		t.Errorf("nudges = %d, want exactly 1", c.nudges)
	}
}

func TestAwaitEffectiveResolversGivesUpAfterTheTimeoutWithAPlainMessage(t *testing.T) {
	c := newFakeResolverClock(-1)
	err := awaitEffectiveResolvers(c.wait(), c.expected())
	if err == nil {
		t.Fatal("want an error when the zone never appears")
	}
	if waited := c.now.Sub(c.start); waited < 20*time.Second || waited > 21*time.Second {
		t.Errorf("waited %v, want about the 20 s timeout", waited)
	}
	msg := err.Error()
	if strings.Contains(msg, "scutil") || strings.Contains(msg, "`") {
		t.Errorf("message must not hand the user a shell command: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "connect again") {
		t.Errorf("message must say what to do next (connect again): %q", msg)
	}
}

func TestAwaitEffectiveResolversReturnsAReadErrorAtOnce(t *testing.T) {
	c := newFakeResolverClock(-1)
	w := c.wait()
	boom := errors.New("scutil failed")
	w.read = func() (map[string][]netip.Addr, error) { return nil, boom }
	err := awaitEffectiveResolvers(w, c.expected())
	if !errors.Is(err, boom) {
		t.Fatalf("want the read error, got %v", err)
	}
	if c.now != c.start {
		t.Errorf("waited %v after a read error, want none", c.now.Sub(c.start))
	}
}
