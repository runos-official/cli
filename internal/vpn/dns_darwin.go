//go:build darwin

package vpn

import (
	"fmt"
	"net/netip"
)

// ReconcileDNS publishes the private DNS the plan needs through both mechanisms, removes what it no
// longer needs, and takes ONE look at whether macOS lists the zones. It never waits: the answer is
// ready, pending or an error, and the daemon decides what a pending zone means (dns_pending.go).
// The order of the two mechanisms and the reason for it are in dns_chain_darwin.go.
func (p darwinPlatform) ReconcileDNS(iface string, clientAddr netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	return p.reconcileDNS(iface, clientAddr, resolvers, false)
}

// RecheckDNS is the background look: the same reconcile, which also puts back whichever mechanism went
// missing, and when republish is set and a zone is still unlisted, publishes both again and flushes
// DNS, so macOS sees a fresh change with the full content. See H2 in FCR 1730: a theory, not a
// measurement.
func (p darwinPlatform) RecheckDNS(iface string, clientAddr netip.Addr, resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	return p.reconcileDNS(iface, clientAddr, resolvers, republish)
}

// effectiveDNS returns the resolvers macOS is using now, by zone, from `scutil --dns`. That needs no
// root.
func (p darwinPlatform) effectiveDNS() (map[string][]netip.Addr, error) {
	if p.scutil != nil {
		out, err := p.scutil()
		if err != nil {
			return nil, fmt.Errorf("read effective macOS DNS: %w", err)
		}
		return parseScutilDNS(out), nil
	}
	out, err := run("scutil", "--dns")
	if err != nil {
		return nil, fmt.Errorf("read effective macOS DNS: %w: %s", err, out)
	}
	return parseScutilDNS(string(out)), nil
}

// allIn reports whether every zone is one of the plans' zones.
func allIn(zones []string, plans []ResolverPlan) bool {
	in := map[string]bool{}
	for _, p := range plans {
		in[p.Zone] = true
	}
	for _, z := range zones {
		if !in[z] {
			return false
		}
	}
	return true
}
