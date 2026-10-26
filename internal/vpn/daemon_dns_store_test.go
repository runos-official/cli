//go:build darwin

package vpn

import (
	"net/netip"
	"testing"
)

// storeBackedPlatform is the daemon's platform with the REAL macOS DNS chain (store, files, the
// scutil --dns check) on fakes, and the routes and interface as no-ops. The watch, the schedule and
// the daemon's locking run for real.
type storeBackedPlatform struct {
	fakePlatform
	mac *chainMac
}

func (p *storeBackedPlatform) ReconcileDNS(iface string, client netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	return p.mac.ReconcileDNS(iface, client, resolvers)
}

func (p *storeBackedPlatform) RecheckDNS(iface string, client netip.Addr, resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	return p.mac.RecheckDNS(iface, client, resolvers, republish)
}

func (p *storeBackedPlatform) Teardown(iface string) error {
	_ = p.fakePlatform.Teardown(iface)
	return p.mac.Teardown(iface)
}

func newStoreHarness(t *testing.T) (*dnsHarness, *chainMac) {
	t.Helper()
	h := newDNSHarness(t)
	mac := newChainMac(t)
	mac.storePickup, mac.filePickup = false, false
	h.d.platform = &storeBackedPlatform{mac: mac}
	return h, mac
}

// The background watch repairs a store entry that went missing, and ends in ready the moment macOS
// lists the zones, with the store named as the mechanism.
func TestTheWatchWorksWithTheStoreMechanism(t *testing.T) {
	h, mac := newStoreHarness(t)

	resp := h.up()
	if resp.Error != "" || resp.Status == nil || !resp.Status.DNS.Pending || resp.Status.DNS.Mode != "store" {
		t.Fatalf("up = %+v, want pending in store mode", resp)
	}
	if len(mac.store.keys) == 0 {
		t.Fatal("the store entries were not written")
	}

	// Another program wipes our entries while the watch is waiting; the next look puts them back.
	for k := range mac.store.keys {
		delete(mac.store.keys, k)
	}
	h.step(t)
	if len(mac.store.keys) == 0 {
		t.Error("the watch did not put the store entries back")
	}

	mac.storePickup = true
	h.stepUntilSettled(t, 10)
	st := h.status()
	if !st.DNS.Available || st.DNS.Pending || st.DNS.Mode != "store" {
		t.Fatalf("dns = %+v, want ready in store mode", st.DNS)
	}
	h.d.Handle(Request{Op: OpDown})
	h.watchGone(t)
}

// Never listed: the watch gives up with the plain text, the mechanism is still named, the tunnel stays
// up, and every goroutine ends.
func TestTheWatchGivesUpPlainlyWithTheStoreMechanismAndLeaksNothing(t *testing.T) {
	h, mac := newStoreHarness(t)
	h.up()

	h.stepUntilSettled(t, 400)

	st := h.status()
	if st.DNS.Available || st.DNS.Pending || st.DNS.Error != dnsGaveUpText || st.DNS.Mode != "store" {
		t.Fatalf("dns = %+v, want the plain give-up state in store mode", st.DNS)
	}
	assertPlainText(t, "gave up", st.DNS.Error)
	if !st.Running || h.engine.closed() != 0 {
		t.Errorf("running=%v closes=%d: a working tunnel was torn down", st.Running, h.engine.closed())
	}
	if len(mac.store.keys) == 0 {
		t.Error("the entries are removed on give-up; they stay until the tunnel stops")
	}
	h.d.Handle(Request{Op: OpDown})
	if len(mac.store.keys) != 0 {
		t.Errorf("keys %v are left in the store after the tunnel stopped", mac.store.sortedKeys())
	}
	h.watchGone(t)
}
