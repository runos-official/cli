package vpn

import (
	"errors"
	"strings"
	"testing"
	"time"
)

/*
A private DNS zone that macOS has not listed yet does not fail the connect (FCR 1730, H3).

Measured on a developer Mac: the tunnel, the address and the routes were up and the handshake had
completed, `up` failed on "private DNS zone is not effective yet", and two minutes later macOS still
listed no resolver for the zone. These tests run the real daemon against a fake tunnel engine and a
fake platform that behaves like the macOS one, with a clock the test steps by hand.
*/

func TestUpSucceedsWhileMacOSHasNotListedThePrivateDNSZones(t *testing.T) {
	h := newDNSHarness(t)

	resp := h.up()

	if resp.Error != "" {
		t.Fatalf("up failed (%q) with the tunnel up: a zone macOS has not listed yet must not fail the connect", resp.Error)
	}
	st := resp.Status
	if st == nil || !st.Running || st.Address == "" {
		t.Fatalf("status = %+v, want a running tunnel with its address", st)
	}
	if !st.DNS.Pending || st.DNS.Available {
		t.Errorf("dns = %+v, want pending and not available", st.DNS)
	}
	assertPlainText(t, "pending status", st.DNS.Error)
	if h.engine.closed() != 0 {
		t.Error("a working tunnel was torn down")
	}
}

func TestPendingBecomesReadyWhenMacOSListsTheZones(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	if !h.status().DNS.Pending {
		t.Fatal("want pending before macOS lists the zones")
	}

	h.platform.listAll(h.zones)
	h.step(t)

	st := h.status()
	if !st.DNS.Available || st.DNS.Pending || st.DNS.Error != "" {
		t.Errorf("dns = %+v, want ready", st.DNS)
	}
}

// Late appearance, on the fake clock: after the old 5 s wait and after the 20 s wait of the first fix.
func TestPendingBecomesReadyWhenTheZonesAppearLate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		looks     int // looks before macOS lists the zones
		wantMinRe int
	}{
		{"after the old five second wait", 11, 1},
		{"after the 20 second wait", 23, 2},
		{"after two minutes", 40, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDNSHarness(t)
			h.up()
			h.step(t) // first look
			for i := 1; i < tc.looks; i++ {
				h.step(t)
			}
			if !h.status().DNS.Pending {
				t.Fatalf("dns = %+v, want still pending before macOS lists the zones", h.status().DNS)
			}

			h.platform.listAll(h.zones)
			h.step(t)

			if st := h.status(); !st.DNS.Available || st.DNS.Pending {
				t.Errorf("dns = %+v, want ready", st.DNS)
			}
			if _, republishes, _ := h.platform.counts(); republishes < tc.wantMinRe {
				t.Errorf("republishes = %d, want at least %d: the file must be re-published more than once", republishes, tc.wantMinRe)
			}
		})
	}
}

// The bound: a zone that never appears ends in a plain failed state, and the tunnel stays up.
func TestPendingEndsInAPlainFailedStateAndKeepsTheTunnel(t *testing.T) {
	h := newDNSHarness(t)
	h.up()

	looks := h.stepUntilSettled(t, 400)

	st := h.status()
	if st.DNS.Pending || st.DNS.Available {
		t.Fatalf("dns = %+v, want a failed state after the bound", st.DNS)
	}
	if st.DNS.Error != dnsGaveUpText {
		t.Errorf("error = %q, want the plain give-up text", st.DNS.Error)
	}
	assertPlainText(t, "failed status", st.DNS.Error)
	if !st.Running || h.engine.closed() != 0 || h.platform.teardownCount() != 0 {
		t.Errorf("running=%v closes=%d teardowns=%d: a working tunnel was torn down", st.Running, h.engine.closed(), h.platform.teardownCount())
	}
	if _, republishes, _ := h.platform.counts(); republishes < 5 {
		t.Errorf("republishes = %d over %d looks, want the file re-published repeatedly", republishes, looks)
	}
	if bound := h.clock.now().Sub(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)); bound < 170*time.Second || bound > 190*time.Second {
		t.Errorf("gave up after %v, want about the 3 minute bound", bound)
	}
}

// A later poll does not restart a watch that gave up, and it picks the zones up if macOS lists them.
func TestAGaveUpWatchIsNotRestartedByAPollAndRecoversWhenMacOSCatchesUp(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.stepUntilSettled(t, 400)
	gaveUp := h.watch()

	h.d.Handle(Request{Op: OpPoll})
	if h.watch() != gaveUp {
		t.Error("a poll restarted the watch: the bound must not reset")
	}
	if st := h.status(); st.DNS.Pending || st.DNS.Error != dnsGaveUpText {
		t.Errorf("dns = %+v, want the give-up state to stay", st.DNS)
	}

	h.platform.listAll(h.zones)
	h.d.Handle(Request{Op: OpPoll})

	if st := h.status(); !st.DNS.Available || st.DNS.Error != "" {
		t.Errorf("dns = %+v, want ready once macOS lists the zones", st.DNS)
	}
}

func TestStoppingTheTunnelEndsTheWatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(h *dnsHarness)
	}{
		{"down", func(h *dnsHarness) { h.d.Handle(Request{Op: OpDown}) }},
		{"daemon close", func(h *dnsHarness) { h.d.Close() }},
		{"session lapsed", func(h *dnsHarness) {
			h.d.mu.Lock()
			h.d.applyLoginRequiredLocked()
			h.d.mu.Unlock()
			h.d.mu.Lock()
			h.d.stopTunnelLocked()
			h.d.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDNSHarness(t)
			h.up()
			h.step(t)
			_, _, before := h.platform.counts()

			tc.stop(h)

			h.watchGone(t)
			if h.watch() != nil {
				t.Error("the watch is still registered after the tunnel stopped")
			}
			if _, _, after := h.platform.counts(); after != before {
				t.Errorf("rechecks went from %d to %d after the stop: the watch touched the machine", before, after)
			}
		})
	}
}

// A second `up` while DNS is pending keeps the tunnel, the files and the clock.
func TestASecondUpWhilePendingIsIdempotent(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.step(t)
	watch := h.watch()
	writesBefore, _, _ := h.platform.counts()

	req := upRequest("acct")
	req.ConductorURL = h.server.URL
	req.SessionToken = "session-2"
	resp := h.d.Handle(req)

	if resp.Error != "" || resp.Status == nil || !resp.Status.DNS.Pending {
		t.Fatalf("resp = %+v, want a pending status and no error", resp)
	}
	if h.creates != 1 || h.engine.closed() != 0 || h.platform.teardownCount() != 0 {
		t.Errorf("creates=%d closes=%d teardowns=%d: the tunnel was rebuilt", h.creates, h.engine.closed(), h.platform.teardownCount())
	}
	if h.watch() != watch {
		t.Error("the watch was replaced: the clock restarted")
	}
	if writes, _, _ := h.platform.counts(); writes != writesBefore {
		t.Errorf("writes went from %d to %d: the resolver files were rewritten", writesBefore, writes)
	}
	if got := h.d.state.Active().SessionToken; got != "session-2" {
		t.Errorf("session token = %q, want the new session kept", got)
	}
}

// A different account is a different tunnel: the shortcut is only for the same device.
func TestAnUpForAnotherAccountWhilePendingRebuildsTheTunnel(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	if _, err := h.d.state.IdentityForAccount("other"); err != nil {
		t.Fatal(err)
	}
	req := upRequest("other")
	req.ConductorURL = h.server.URL

	h.d.Handle(req)

	if h.creates != 2 {
		t.Errorf("creates = %d, want the tunnel rebuilt for the other account", h.creates)
	}
}

// A poll re-asserts the plan. While pending that must not rewrite files or restart the clock.
func TestAPollWhilePendingKeepsTheWatchAndWritesNothing(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.step(t)
	watch := h.watch()
	writesBefore, _, _ := h.platform.counts()

	h.d.Handle(Request{Op: OpPoll})

	if h.watch() != watch {
		t.Error("the poll replaced the watch")
	}
	if writes, _, _ := h.platform.counts(); writes != writesBefore {
		t.Errorf("writes went from %d to %d on a poll with an unchanged plan", writesBefore, writes)
	}
}

// Two zones, one listed: still pending until both are.
func TestPendingStaysUntilEveryZoneIsListed(t *testing.T) {
	h := newDNSHarness(t)
	if len(h.zones) < 2 {
		t.Fatalf("the sample document must carry at least two zones, got %d", len(h.zones))
	}
	h.platform.list(h.zones[0].Zone, h.zones[0].Resolver)
	h.up()
	h.step(t)
	if st := h.status(); !st.DNS.Pending || st.DNS.Available {
		t.Fatalf("dns = %+v, want pending with only one zone listed", st.DNS)
	}

	h.platform.listAll(h.zones)
	h.step(t)

	if st := h.status(); !st.DNS.Available {
		t.Errorf("dns = %+v, want ready", st.DNS)
	}
}

// Another resolver that keeps answering for the zone is a real fault: the watch reports it and stops.
func TestAnOverrideThatPersistsEndsTheWatchWithTheFault(t *testing.T) {
	h := newDNSHarness(t)
	h.up()
	h.platform.list(h.zones[0].Zone, mustAddr("198.51.100.9"))

	h.step(t)

	st := h.status()
	if st.DNS.Pending || st.DNS.Available || !strings.Contains(st.DNS.Error, "overridden") {
		t.Errorf("dns = %+v, want the override reported", st.DNS)
	}
	select {
	case <-h.watch().settled:
	default:
		t.Error("the watch keeps running after a fault that waiting cannot fix")
	}
}

// A restart with a stored session sweeps the leftovers, then applies the plan again and verifies it.
func TestRestartWithAStoredSessionSweepsThenReappliesAndVerifies(t *testing.T) {
	h := newDNSHarness(t)
	// The previous daemon left a file and a listing behind.
	h.platform.written[h.zones[0].Zone] = h.zones[0].Resolver
	h.platform.list(h.zones[0].Zone, h.zones[0].Resolver)
	activeSession(t, h.d, time.Now().Add(time.Hour))
	h.d.state.Active().ConductorURL = h.server.URL
	h.d.state.Active().DeviceID = "device-1"

	logged := captureLog(t, h.d.Resume)

	if !strings.Contains(logged, "start-up sweep") {
		t.Errorf("log lacks the sweep line:\n%s", logged)
	}
	if h.platform.teardownCount() != 1 || h.creates != 1 {
		t.Fatalf("teardowns=%d creates=%d, want one sweep and one tunnel", h.platform.teardownCount(), h.creates)
	}
	if st := h.status(); !st.Running || !st.DNS.Pending {
		t.Fatalf("status = %+v, want a running tunnel with DNS pending (the sweep removed the old listing)", st)
	}
	if writes, _, _ := h.platform.counts(); writes != len(h.zones) {
		t.Errorf("writes = %d, want every zone written again after the sweep (%d)", writes, len(h.zones))
	}

	h.platform.listAll(h.zones)
	h.step(t)

	if st := h.status(); !st.DNS.Available {
		t.Errorf("dns = %+v, want ready after the restart", st.DNS)
	}
}

// H4: the daemon answers while a connect waits for DNS. The wait is outside the lock, and the control
// socket answers a status request meanwhile.
func TestControlSocketAnswersWhileAConnectWaitsForDNS(t *testing.T) {
	h := newDNSHarness(t)
	h.d.dnsTiming.upWait = 30 * time.Second // far longer than the test
	sock := shortSocketPath(t, "runos-vpn-dns.sock")
	listener, err := Serve(h.d, sock, "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	upDone := make(chan Response, 1)
	go func() { upDone <- h.up() }()
	deadline := time.Now().Add(5 * time.Second)
	for h.watch() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the connect never reached its DNS wait")
		}
		time.Sleep(2 * time.Millisecond)
	}

	started := time.Now()
	resp, err := NewClient(sock).Call(Request{Op: OpStatus})
	if err != nil {
		t.Fatalf("status over the control socket during the wait: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("status took %v during a DNS wait, want it answered at once", elapsed)
	}
	if resp.Status == nil || !resp.Status.Running || !resp.Status.DNS.Pending {
		t.Errorf("status = %+v, want a running tunnel with DNS pending", resp.Status)
	}
	select {
	case <-upDone:
		t.Fatal("the connect answered before DNS settled or the wait ended")
	default:
	}

	h.platform.listAll(h.zones)
	h.step(t)
	select {
	case r := <-upDone:
		if r.Error != "" || r.Status == nil || !r.Status.DNS.Available {
			t.Errorf("connect = %+v, want ready once macOS lists the zones", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connect did not answer after DNS settled")
	}
}

// A connect that waits briefly and then answers pending: the answer is not held for the whole bound.
func TestAConnectAnswersPendingAfterTheShortWait(t *testing.T) {
	h := newDNSHarness(t)
	started := time.Now()

	resp := h.up()

	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("up took %v, want it to answer after the short wait", elapsed)
	}
	if resp.Status == nil || !resp.Status.DNS.Pending {
		t.Errorf("resp = %+v, want pending", resp)
	}
}

// A fault from the platform (not a delay) still fails the connect, as before, and keeps the tunnel.
func TestADNSFaultStillFailsTheConnectAndKeepsTheTunnel(t *testing.T) {
	h := newDNSHarness(t)
	h.platform.reconcileErr = errors.New("private DNS zone is overridden by another resolver")

	resp := h.up()

	if !strings.Contains(resp.Error, "overridden") {
		t.Fatalf("error = %q, want the fault", resp.Error)
	}
	st := h.status()
	if !st.Running || st.DNS.Available || st.DNS.Pending || st.DNS.Error == "" {
		t.Errorf("status = %+v, want a running tunnel and a failed DNS state with its reason", st)
	}
	if h.watch() != nil {
		t.Error("a watch started for a fault that waiting cannot fix")
	}
}
