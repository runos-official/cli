package vpn

import (
	"context"

	"fmt"
	"net/netip"
	"strings"
	"time"
)

/*
A private DNS zone that macOS has not listed yet does not fail the connect.

THE DEFECT (FCR 1730). `up` returned an error, the CLI reported a failed handoff and RunOS Desktop
said it could not connect, while the tunnel, the address and the routes were up and the handshake had
completed. The only thing missing was macOS listing the zone in `scutil --dns`, which it does on its
own schedule: under a second on one run, and not within two minutes on another. A connect that is
failed by something outside its control, with a working tunnel underneath, is the wrong answer.

THE RULE. The platform reports ready, pending or a fault (ReconcileDNS). A fault still fails the
step. Pending leaves the connect successful and starts ONE background watch for the current set of
zones, which keeps looking, re-publishes the resolver files, and ends in ready or in a plain
"gave up" state after a bound (dns_schedule.go). It never tears a working tunnel down.

MUTEX RULES. The watch holds d.mu only for one look at a time (one scutil call and, rarely, a file
rewrite), so a status request is never held behind it. It checks its own context after taking the
lock, so a look that was waiting while the tunnel stopped does not touch the machine: stopping
cancels it under the same lock that tears the platform down.
*/

// dnsWatch is the background check for one set of zones.
type dnsWatch struct {
	cancel context.CancelFunc
	// key identifies the set of zones and resolvers being watched; a changed plan starts a new watch.
	key string
	// settled is closed when the watch ends for any reason, so a connect can wait for the usual
	// quick outcome without holding the daemon's lock.
	settled chan struct{}
	// ended and gaveUp are written under d.mu.
	ended, gaveUp bool
}

// dnsTiming holds the clock and the schedule of the watch. Its zero value is the real one.
type dnsTiming struct {
	schedule dnsSchedule
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) bool
	// upWait is how long a connect waits for a pending zone to settle before it answers.
	upWait time.Duration
}

// defaultDNSUpWait is how long a connect waits for macOS to list the zones. Most republishes finish
// inside it, so most connects say "ready"; a slow one says "pending" and finishes in the background.
const defaultDNSUpWait = 4 * time.Second

func (t dnsTiming) loop() dnsWatchLoop {
	l := dnsWatchLoop{schedule: t.schedule, now: t.now, sleep: t.sleep}
	if l.schedule == (dnsSchedule{}) {
		l.schedule = defaultDNSSchedule
	}
	if l.now == nil {
		l.now = time.Now
	}
	if l.sleep == nil {
		l.sleep = sleepContext
	}
	return l
}

func (t dnsTiming) upWaitOrDefault() time.Duration {
	if t.upWait == 0 {
		return defaultDNSUpWait
	}
	return t.upWait
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// dnsRechecker is the part of a platform that can look again and re-publish. A platform without it
// (Linux, Windows) never reports a pending zone.
type dnsRechecker interface {
	RecheckDNS(resolvers []ResolverPlan, republish bool) (DNSStatus, error)
}

func resolverKey(resolvers []ResolverPlan) string {
	parts := make([]string, len(resolvers))
	for i, r := range resolvers {
		parts[i] = r.Zone + "=" + r.Resolver.String()
	}
	return strings.Join(parts, ",")
}

// applyDNSResultLocked records what the platform said about DNS and decides what happens next. It
// returns an error only for a fault, never for a pending zone.
func (d *Daemon) applyDNSResultLocked(dns DNSStatus, err error, resolvers []ResolverPlan) error {
	switch {
	case err != nil:
		d.stopDNSWatchLocked()
		d.dns = dns
		stepOutcome("dns", err, "")
		return err
	case dns.Pending:
		d.dns = d.holdPendingLocked(dns, resolvers)
		return nil
	default:
		d.stopDNSWatchLocked()
		d.dns = dns
		stepOutcome("dns", nil, "mode="+dns.Mode)
		return nil
	}
}

// holdPendingLocked makes sure a watch exists for these zones and returns the status to show. A
// watch that already runs keeps its clock: a re-assert on the next poll must not restart the bound.
func (d *Daemon) holdPendingLocked(pending DNSStatus, resolvers []ResolverPlan) DNSStatus {
	key := resolverKey(resolvers)
	if w := d.dnsWatch; w != nil && w.key == key && (!w.ended || w.gaveUp) {
		if w.gaveUp {
			return DNSStatus{Mode: pending.Mode, Error: dnsGaveUpText}
		}
		return pending
	}
	d.stopDNSWatchLocked()
	ctx, cancel := context.WithCancel(context.Background())
	w := &dnsWatch{cancel: cancel, key: key, settled: make(chan struct{})}
	d.dnsWatch = w
	logStepLine(stepBody("dns", statusWarn, "macOS has not listed the private DNS zones yet; checking in the background"))
	d.wg.Add(1)
	go d.runDNSWatch(ctx, w, resolvers)
	return pending
}

// stopDNSWatchLocked ends the watch, if any. It only cancels: the goroutine takes d.mu, which the
// caller holds, so waiting for it here would deadlock. Close waits for it on d.wg.
func (d *Daemon) stopDNSWatchLocked() {
	if d.dnsWatch != nil {
		d.dnsWatch.cancel()
		d.dnsWatch = nil
	}
}

func (d *Daemon) runDNSWatch(ctx context.Context, w *dnsWatch, resolvers []ResolverPlan) {
	defer d.wg.Done()
	defer close(w.settled)
	end, _, _ := d.dnsTiming.loop().run(ctx, func(republish bool) (DNSStatus, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if ctx.Err() != nil || d.dnsWatch != w || d.engine == nil {
			return DNSStatus{}, errStopping
		}
		status, err := d.recheckDNSLocked(resolvers, republish)
		switch {
		case err != nil:
			d.dns = status
			stepOutcome("dns", err, "")
		case status.Available:
			d.dns = status
			stepOutcome("dns", nil, "mode="+status.Mode)
		default:
			d.dns = status
		}
		return status, err
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dnsWatch != w {
		return
	}
	w.ended = true
	if end == dnsEndGaveUp {
		w.gaveUp = true
		d.dns = DNSStatus{Mode: "native", Error: dnsGaveUpText}
		stepOutcome("dns", fmt.Errorf("macOS did not list the private DNS zones within %s", d.dnsTiming.loop().schedule.bound), "")
	}
}

// recheckDNSLocked is one look at the platform, with a re-publish when asked.
func (d *Daemon) recheckDNSLocked(resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	if r, ok := d.platform.(dnsRechecker); ok {
		return r.RecheckDNS(resolvers, republish)
	}
	iface := ""
	if d.engine != nil {
		iface = d.engine.InterfaceName()
	}
	var client netip.Addr
	if d.plan.Address.IsValid() {
		client = d.plan.Address.Addr()
	}
	return d.platform.ReconcileDNS(iface, client, resolvers)
}

// settleDNS waits, without the daemon's lock, for a pending zone to settle, and returns the response
// with a status read afterwards. A status request, a poll tick or a stop all take the lock freely
// meanwhile. The wait is short on purpose: the watch carries on after the answer.
func (d *Daemon) settleDNS(resp Response, w *dnsWatch) Response {
	timer := time.NewTimer(d.dnsTiming.upWaitOrDefault())
	defer timer.Stop()
	select {
	case <-w.settled:
	case <-timer.C:
	}
	if resp.Status == nil {
		return resp
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	resp.Status = d.statusLocked()
	return resp
}

// pendingWatchLocked returns the watch a connect should wait for, or nil.
func (d *Daemon) pendingWatchLocked() *dnsWatch {
	if d.dnsWatch != nil && !d.dnsWatch.ended && d.dns.Pending {
		return d.dnsWatch
	}
	return nil
}

// upWhilePendingLocked answers a second `up` for the same account and device while the tunnel is up
// and DNS is pending. It must not rebuild the tunnel: that removes the resolver files, writes them
// again and restarts the clock, which is exactly what made "connect again" look like the cure
// without being one. It takes the fresh session so a lapsing one is not left behind.
func (d *Daemon) upWhilePendingLocked(req Request) (Response, bool) {
	active := d.state.Active()
	if d.engine == nil || d.pendingWatchLocked() == nil || active == nil ||
		active.AccountID != req.AccountID || active.DeviceID != req.DeviceID {
		return Response{}, false
	}
	active.SessionToken = req.SessionToken
	active.SessionExpiresAt = req.SessionExpiresAt
	active.ConductorURL = req.ConductorURL
	if err := SaveState(d.stateDir, d.state); err != nil {
		return Response{Error: err.Error()}, true
	}
	d.client = newConductorClient(active.ConductorURL, active.AccountID, active.DeviceID, active.SessionToken)
	logEvent("up requested again while the private DNS zones are still being applied: keeping the tunnel")
	return Response{Status: d.statusLocked()}, true
}
