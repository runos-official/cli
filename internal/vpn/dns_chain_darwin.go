//go:build darwin

package vpn

import "net/netip"

/*
The fallback chain for macOS private DNS (FCR 1730), and why it is in this order.

THE EVIDENCE, measured on a developer Mac running macOS 26.6: the daemon wrote /etc/resolver/<zone>
atomically with exactly the content macOS expects, the tunnel and the routes were up, and the
resolver answered through the tunnel, yet `scutil --dns` never listed the zone, not after five
minutes, not after re-publishing the file every 15 seconds with a DNS flush. A non-root
network-change notification changed nothing, and the logs of configd and mDNSResponder said
nothing about the file. On earlier macOS versions, and earlier the same day, a retry sometimes made
the file work. So the file door is flaky, not impossible.

THE ORDER.
 1. The dynamic-store entry (dns_store.go) is PRIMARY. It is the door VPN clients use, it goes to
    configd directly instead of waiting for a directory watch to fire, and it is the one mechanism
    this daemon can read back and prove. NOT MEASURED: nothing about how fast macOS picks it up
    could be measured without root; `make dnsprobe` (method D) measures it on a Mac.
 2. The /etc/resolver file stays SECOND. Some tools and older macOS versions read it, and it is the
    mechanism that has worked in the past. It costs one atomic write.
 3. Readiness is what `scutil --dns` LISTS with the cluster's resolver, never what was written. A
    write returning, a file existing or a key being in the store is not proof.
 4. If the two disagree the store wins: a resolver file of our own that cannot be brought in line is
    removed, so it cannot answer for the zone with a stale address. A hand-authored file is never
    touched (the standing rule), and if it makes another resolver answer, that is reported as the
    override it is.

WHAT FAILS WHAT.
 - the store works, the file does not: not an error; the store carries the zones (mode "store").
 - the store fails, the file works: not an error; the file carries them (mode "native"), and the
   store is tried again on every look of the background watch.
 - both fail to write: a plain error. The daemon keeps the tunnel and its poll loop, which asks again.
 - the zones are written but not listed: pending, and the background watch repairs and re-publishes
   until the bound (dns_schedule.go), then reports a plain "gave up".
 - `scutil --dns` cannot be read (the binary is missing or errors): unverified, never failed.
*/

// reconcileDNS is one pass of the chain. republish, set by the background watch, asks for both
// mechanisms to be published again when a zone is still unlisted after the check.
func (p darwinPlatform) reconcileDNS(iface string, client netip.Addr, resolvers []ResolverPlan, republish bool) (DNSStatus, error) {
	if len(resolvers) == 0 {
		return p.clearDNS()
	}
	storeChanged, storeErr := p.ensureStore(iface, client, resolvers, false)
	if storeErr != nil {
		logEvent("dns: dynamic store: %s", storeErr)
	}
	storeOK := storeErr == nil
	diff, fileErr := p.ensureFiles(resolvers)
	if fileErr != nil && storeOK {
		// The store wins: a file of ours that could not be brought in line must not keep answering.
		logEvent("dns: the resolver file could not be written; the dynamic store carries the zones")
		p.removeStaleOwnFiles(resolvers)
		fileErr = nil
	}
	if fileErr != nil {
		return DNSStatus{Mode: "unavailable", Error: fileErr.Error()}, fileErr
	}
	if storeChanged || len(diff.Set) > 0 || len(diff.Remove) > 0 {
		_ = p.flushDNS()
	}
	mode := "native"
	if storeOK {
		mode = "store"
	}

	effective, err := p.effectiveDNS()
	if err != nil {
		// Unverified is not failed: the zones are published and nothing says they did not work. Report
		// it plainly and let the next poll look again, rather than fail a connect over it.
		logEvent("dns: %s", err)
		return DNSStatus{Mode: "unavailable", Error: dnsUnreadableText}, nil
	}
	check := checkEffectiveResolvers(effective, resolvers)
	// A zone this call has just published may still be listed with its OLD resolver until macOS
	// republishes. That is a delay, and the next look reports it as an override if it persists.
	if check.Overridden != nil && (storeChanged || allIn(check.OverriddenZones, diff.Set)) {
		check.Pending = append(check.Pending, check.OverriddenZones...)
		check.Overridden = nil
	}
	switch {
	case check.Overridden != nil:
		return DNSStatus{Mode: "unavailable", Error: check.Overridden.Error()}, check.Overridden
	case check.OK():
		return DNSStatus{Available: true, Mode: mode}, nil
	}
	if republish {
		if err := p.republish(iface, client, resolvers, check.Pending, storeOK); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	return DNSStatus{Mode: mode, Pending: true, Error: dnsPendingText}, nil
}

// ensureFiles brings the resolver files to the plan. A write that fails stops the pass and is
// returned; the caller decides whether the store makes it harmless.
func (p darwinPlatform) ensureFiles(resolvers []ResolverPlan) (ResolverDiff, error) {
	have, err := p.Resolvers()
	if err != nil {
		return ResolverDiff{}, err
	}
	diff := DiffResolvers(have, resolvers)
	for _, resolver := range diff.Set {
		if err := p.SetResolver(resolver.Zone, resolver.Resolver); err != nil {
			return diff, err
		}
	}
	for _, zone := range diff.Remove {
		if err := p.RemoveResolver(zone); err != nil {
			return diff, err
		}
	}
	return diff, nil
}

// removeStaleOwnFiles removes our own resolver file for each planned zone whose content is not what
// the plan says. A file that is not ours, or already right, stays.
func (p darwinPlatform) removeStaleOwnFiles(resolvers []ResolverPlan) {
	have, err := p.Resolvers()
	if err != nil {
		return
	}
	for _, plan := range resolvers {
		if addr, ok := have[plan.Zone]; ok && addr != plan.Resolver {
			if err := p.RemoveResolver(plan.Zone); err != nil {
				logEvent("dns: %s", err)
			}
		}
	}
}

// clearDNS is the pass for a plan with no zones: both mechanisms are emptied.
func (p darwinPlatform) clearDNS() (DNSStatus, error) {
	diff, err := p.ensureFiles(nil)
	if err != nil {
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	removed, storeErr := p.sweepStore()
	if storeErr != nil {
		logEvent("dns: dynamic store: %s", storeErr)
	}
	if len(diff.Remove) > 0 || removed > 0 {
		_ = p.flushDNS()
	}
	return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
}

// republish publishes both mechanisms again for the zones macOS has not listed and flushes DNS. The
// store entry is taken out and put back; the file is rewritten atomically. A file failure is an error
// only when the store is not carrying the zones.
func (p darwinPlatform) republish(iface string, client netip.Addr, resolvers []ResolverPlan, zones []string, storeOK bool) error {
	if _, err := p.ensureStore(iface, client, resolvers, true); err != nil {
		logEvent("dns: dynamic store: %s", err)
		storeOK = false
	}
	pending := map[string]bool{}
	for _, zone := range zones {
		pending[zone] = true
	}
	var fileErr error
	for _, resolver := range resolvers {
		if pending[resolver.Zone] {
			if err := p.SetResolver(resolver.Zone, resolver.Resolver); err != nil {
				if fileErr == nil {
					fileErr = err
				}
			}
		}
	}
	_ = p.flushDNS()
	if fileErr != nil && !storeOK {
		return fileErr
	}
	return nil
}
