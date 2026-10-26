//go:build darwin

package vpn

import (
	"fmt"
	"net/netip"
)

// ReconcileDNS writes the resolver files the plan needs, removes the ones it no longer needs, and
// takes ONE look at whether macOS lists them. It never waits: the answer is ready, pending or an
// error, and the daemon decides what a pending zone means (dns_pending.go).
func (p darwinPlatform) ReconcileDNS(_ string, _ netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	return p.reconcileDNS(resolvers, false)
}

// RecheckDNS is the background look: the same reconcile, and when republish is set and a zone is
// still unlisted, it rewrites that zone's file atomically and flushes DNS, so macOS sees a fresh
// directory event with the full content. See H2 in FCR 1730: a theory, not a measurement.
func (p darwinPlatform) RecheckDNS(resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	return p.reconcileDNS(resolvers, republish)
}

func (p darwinPlatform) reconcileDNS(resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	have, err := p.Resolvers()
	if err != nil {
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	diff := DiffResolvers(have, resolvers)
	for _, resolver := range diff.Set {
		if err := p.SetResolver(resolver.Zone, resolver.Resolver); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	for _, zone := range diff.Remove {
		if err := p.RemoveResolver(zone); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	if len(diff.Set) > 0 || len(diff.Remove) > 0 {
		_ = p.flushDNS()
	}
	if len(resolvers) == 0 {
		return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
	}

	effective, err := p.effectiveDNS()
	if err != nil {
		logEvent("dns: %s", err)
		return DNSStatus{Mode: "unavailable", Error: dnsUnreadableText}, fmt.Errorf("%s", dnsUnreadableText)
	}
	check := checkEffectiveResolvers(effective, resolvers)
	// A zone this call has just rewritten may still be listed with its OLD resolver until macOS
	// republishes. That is a delay, and the next look reports it as an override if it persists.
	if check.Overridden != nil && allIn(check.OverriddenZones, diff.Set) {
		check.Pending = append(check.Pending, check.OverriddenZones...)
		check.Overridden = nil
	}
	switch {
	case check.Overridden != nil:
		return DNSStatus{Mode: "unavailable", Error: check.Overridden.Error()}, check.Overridden
	case check.OK():
		return DNSStatus{Available: true, Mode: "native"}, nil
	}
	if republish {
		if err := p.republish(check.Pending, resolvers); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	return DNSStatus{Mode: "native", Pending: true, Error: dnsPendingText}, nil
}

// republish rewrites the files of the zones macOS has not listed and flushes DNS.
func (p darwinPlatform) republish(zones []string, resolvers []ResolverPlan) error {
	pending := map[string]bool{}
	for _, zone := range zones {
		pending[zone] = true
	}
	for _, resolver := range resolvers {
		if pending[resolver.Zone] {
			if err := p.SetResolver(resolver.Zone, resolver.Resolver); err != nil {
				return err
			}
		}
	}
	_ = p.flushDNS()
	return nil
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
