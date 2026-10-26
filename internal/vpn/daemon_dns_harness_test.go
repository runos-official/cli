package vpn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// The harness for daemon-level DNS tests: a fake tunnel engine, a fake platform that behaves like the
// macOS one (writes, lists, re-publishes), a local Conductor that serves one document, and a clock
// the test steps by hand. Nothing here touches the machine or the network.

type fakeEngine struct {
	mu     sync.Mutex
	closes int
	applyE error
	upE    error
}

func (*fakeEngine) InterfaceName() string { return "utun9" }
func (e *fakeEngine) Up() error           { return e.upE }
func (e *fakeEngine) ApplyPlan(string, Plan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applyE
}
func (*fakeEngine) Stats() (map[string]*PeerStats, error) { return nil, nil }
func (e *fakeEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closes++
}
func (e *fakeEngine) closed() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closes
}

// dnsFakePlatform models the macOS platform from the daemon's side. The test decides which zones
// macOS "lists"; a zone is never listed before its file was written.
type dnsFakePlatform struct {
	fakePlatform
	mu           sync.Mutex
	written      map[string]netip.Addr // zone -> resolver in our file
	listed       map[string]netip.Addr // zone -> resolver macOS lists
	writes       int
	republishes  int
	rechecks     int
	reconciles   int
	routeErr     error
	addrErr      error
	reconcileErr error
	teardownErr  error
	// appearAfterRepublishes: a zone is listed only after this many re-publishes (0: when the test lists it).
	blockReconcile chan struct{}
}

func newDNSFakePlatform() *dnsFakePlatform {
	return &dnsFakePlatform{written: map[string]netip.Addr{}, listed: map[string]netip.Addr{}}
}

func (p *dnsFakePlatform) list(zone string, resolver netip.Addr) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listed[zone] = resolver
}

func (p *dnsFakePlatform) listAll(resolvers []ResolverPlan) {
	for _, r := range resolvers {
		p.list(r.Zone, r.Resolver)
	}
}

func (p *dnsFakePlatform) counts() (writes, republishes, rechecks int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes, p.republishes, p.rechecks
}

func (p *dnsFakePlatform) SetInterfaceAddress(string, netip.Prefix) error { return p.addrErr }
func (p *dnsFakePlatform) AddRoute(string, netip.Prefix) error            { return p.routeErr }

func (p *dnsFakePlatform) Teardown(iface string) error {
	_ = p.fakePlatform.Teardown(iface)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = map[string]netip.Addr{}
	p.listed = map[string]netip.Addr{}
	return p.teardownErr
}

func (p *dnsFakePlatform) ReconcileDNS(_ string, _ netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	if p.blockReconcile != nil {
		<-p.blockReconcile
	}
	p.mu.Lock()
	p.reconciles++
	p.mu.Unlock()
	return p.look(resolvers, false, false)
}

func (p *dnsFakePlatform) RecheckDNS(_ string, _ netip.Addr, resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	p.mu.Lock()
	p.rechecks++
	p.mu.Unlock()
	return p.look(resolvers, republish, true)
}

func (p *dnsFakePlatform) look(resolvers []ResolverPlan, republish, recheck bool) (DNSStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reconcileErr != nil {
		return DNSStatus{Mode: "unavailable", Error: p.reconcileErr.Error()}, p.reconcileErr
	}
	wanted := map[string]bool{}
	var justWritten []string
	for _, r := range resolvers {
		wanted[r.Zone] = true
		if p.written[r.Zone] != r.Resolver {
			p.written[r.Zone] = r.Resolver
			p.writes++
			justWritten = append(justWritten, r.Zone)
		}
	}
	for zone := range p.written {
		if !wanted[zone] {
			delete(p.written, zone)
		}
	}
	if len(resolvers) == 0 {
		return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
	}
	effective := map[string][]netip.Addr{}
	for zone, addr := range p.listed {
		effective[zone] = []netip.Addr{addr}
	}
	check := checkEffectiveResolvers(effective, resolvers)
	if check.Overridden != nil && allZonesIn(check.OverriddenZones, justWritten) {
		check.Pending, check.Overridden = append(check.Pending, check.OverriddenZones...), nil
	}
	switch {
	case check.Overridden != nil:
		return DNSStatus{Mode: "unavailable", Error: check.Overridden.Error()}, check.Overridden
	case check.OK():
		return DNSStatus{Available: true, Mode: "native"}, nil
	}
	if republish && recheck {
		p.republishes++
		p.writes += len(check.Pending)
	}
	return DNSStatus{Mode: "native", Pending: true, Error: dnsPendingText}, nil
}

func allZonesIn(zones, set []string) bool {
	in := map[string]bool{}
	for _, z := range set {
		in[z] = true
	}
	for _, z := range zones {
		if !in[z] {
			return false
		}
	}
	return true
}

// steppedClock is the watch's clock. The watch parks in sleep and signals it; the test lets it go
// one step at a time, and the fake time moves by exactly the requested gap.
type steppedClock struct {
	mu     sync.Mutex
	t      time.Time
	signal chan struct{} // capacity 1: the watch is parked
	step   chan struct{}
}

func newSteppedClock() *steppedClock {
	return &steppedClock{
		t:      time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
		signal: make(chan struct{}, 1), step: make(chan struct{}),
	}
}

func (c *steppedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *steppedClock) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case c.signal <- struct{}{}:
	default:
	}
	select {
	case <-c.step:
		c.mu.Lock()
		c.t = c.t.Add(d)
		c.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

// dnsHarness is a daemon wired to the fakes, with a session for account "acct" and a Conductor that
// serves the sample document.
type dnsHarness struct {
	d        *Daemon
	platform *dnsFakePlatform
	engine   *fakeEngine
	clock    *steppedClock
	creates  int
	server   *httptest.Server
	zones    []ResolverPlan
}

func newDNSHarness(t *testing.T) *dnsHarness {
	t.Helper()
	h := &dnsHarness{platform: newDNSFakePlatform(), engine: &fakeEngine{}, clock: newSteppedClock()}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "abc" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleDocument))
	}))
	t.Cleanup(h.server.Close)

	previous := createEngine
	createEngine = func(string, bool) (tunnelEngine, error) {
		h.creates++
		return h.engine, nil
	}
	t.Cleanup(func() { createEngine = previous })

	h.d = newTestDaemon(t)
	h.d.platform = h.platform
	h.d.pollInterval = time.Hour
	h.d.dnsTiming = dnsTiming{now: h.clock.now, sleep: h.clock.sleep, upWait: 40 * time.Millisecond}
	if _, err := h.d.state.IdentityForAccount("acct"); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(mustDoc(t, sampleDocument))
	if err != nil {
		t.Fatal(err)
	}
	h.zones = plan.Resolvers
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() { h.d.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("the daemon did not close: a goroutine of the DNS watch is still running")
		}
	})
	return h
}

func (h *dnsHarness) up() Response {
	req := upRequest("acct")
	req.ConductorURL = h.server.URL
	return h.d.Handle(req)
}

func (h *dnsHarness) watch() *dnsWatch {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return h.d.dnsWatch
}

// step lets the parked watch run one look and returns once that look is finished: the watch is
// parked again or has ended.
func (h *dnsHarness) step(t *testing.T) {
	t.Helper()
	w := h.watch()
	if w == nil {
		t.Fatal("no DNS watch to step")
	}
	select {
	case <-h.clock.signal:
	default:
	}
	select {
	case h.clock.step <- struct{}{}:
	case <-w.settled:
		return
	case <-time.After(5 * time.Second):
		t.Fatal("the DNS watch never reached its next wait")
	}
	select {
	case <-h.clock.signal:
	case <-w.settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the DNS watch did not finish its look")
	}
}

// stepUntilSettled steps the watch until it ends, at most max looks.
func (h *dnsHarness) stepUntilSettled(t *testing.T, max int) int {
	t.Helper()
	w := h.watch()
	for i := 0; i < max; i++ {
		select {
		case <-w.settled:
			return i
		default:
		}
		h.step(t)
	}
	select {
	case <-w.settled:
		return max
	default:
		t.Fatalf("the DNS watch was still running after %d looks", max)
		return max
	}
}

func (h *dnsHarness) status() *Status { return h.d.Handle(Request{Op: OpStatus}).Status }

// watchGone fails the test unless every goroutine the daemon started has ended.
func (h *dnsHarness) watchGone(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { h.d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a goroutine of the daemon is still running")
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

var errDisk = errors.New("write resolver: permission denied")
