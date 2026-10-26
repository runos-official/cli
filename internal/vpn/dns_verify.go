package vpn

import (
	"fmt"
	"net/netip"
	"time"
)

// verifyEffectiveResolvers checks that the private DNS zones this session needs are the ones macOS
// will actually use.
//
// It is a single look. Writing /etc/resolver/<zone> does not take effect the instant the file
// lands: macOS re-reads the directory and republishes its DNS configuration asynchronously, so a
// check right after the write sees the zone missing. Callers wait with awaitEffectiveResolvers.
//
// Measured on a developer Mac: a connect failed on this check and the same command succeeded
// moments later, with the file on disk and `scutil --dns` already listing the zone. Nothing was
// wrong with the cluster. Right after a fresh sign-in, when the machine is busy, the republish took
// longer than the five seconds this wait used to allow, so the first Connect in RunOS Desktop failed
// and the second one worked.
func verifyEffectiveResolvers(effective map[string][]netip.Addr, expected []ResolverPlan) error {
	for _, plan := range expected {
		servers := effective[plan.Zone]
		if len(servers) == 0 {
			return fmt.Errorf("macOS has not finished applying the private DNS settings for this " +
				"cluster. This usually clears within seconds: connect again")
		}
		for _, server := range servers {
			if server != plan.Resolver {
				return fmt.Errorf("private DNS zone %s is overridden by resolver %s", plan.Zone, server)
			}
		}
	}
	return nil
}

// resolverWait is awaitEffectiveResolvers' view of the machine, so a test can run it on a fake clock.
type resolverWait struct {
	// read returns the resolvers macOS is using now, by zone.
	read func() (map[string][]netip.Addr, error)
	// nudge asks macOS to republish its DNS configuration. Called once, after nudgeAfter.
	nudge func()
	now   func() time.Time
	sleep func(time.Duration)
	// timeout bounds the wait: a zone that never appears is a real fault and must still be reported.
	timeout, poll, nudgeAfter time.Duration
}

// awaitEffectiveResolvers polls until every expected zone is the one macOS uses, or the timeout
// passes. A read error ends the wait at once. When the timeout passes it returns the last
// verification error, which says what to do next.
func awaitEffectiveResolvers(w resolverWait, expected []ResolverPlan) error {
	start := w.now()
	nudged := false
	for {
		effective, err := w.read()
		if err != nil {
			return err
		}
		verr := verifyEffectiveResolvers(effective, expected)
		if verr == nil {
			return nil
		}
		elapsed := w.now().Sub(start)
		if elapsed >= w.timeout {
			return verr
		}
		if !nudged && elapsed >= w.nudgeAfter {
			nudged = true
			w.nudge()
		}
		w.sleep(w.poll)
	}
}
