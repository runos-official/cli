package vpn

import (
	"fmt"
	"net/netip"
	"sort"
)

// What the person is told while macOS applies the private DNS settings, and when it gives up. These
// are shown unchanged by the CLI and by RunOS Desktop, so they stay short and plain: no command, no
// quoted syntax and no zone name. A zone name is the person's own cluster domain, and a command only
// helps someone who is at a terminal.
const (
	// dnsPendingText is DNSStatus.Error while Pending is true. It stays in Error so a CLI that
	// predates the Pending field still prints a reason after "Private DNS: unavailable -".
	dnsPendingText = "cluster names are still being applied by macOS"
	// DNSPendingNotice is the sentence a connect prints when the tunnel is up and DNS is not yet.
	DNSPendingNotice = "Connected. Cluster names are still being applied by macOS and may take a moment to resolve."
	// dnsGaveUpText is DNSStatus.Error when macOS never listed the zones within the bound.
	dnsGaveUpText = "macOS did not apply the private DNS settings for this cluster. " +
		"The VPN is connected, but cluster names may not resolve. Turn the VPN off and on again."
	// dnsDownText is DNSStatus.Error while no tunnel exists.
	dnsDownText = "the VPN is down"
	// dnsNotSetUpText is DNSStatus.Error from the moment a tunnel exists until DNS has been applied
	// once, so a step that fails before DNS never leaves "the VPN is down" on a tunnel that is up.
	dnsNotSetUpText = "private DNS has not been set up yet"
	// dnsWriteText and dnsRemoveText are DNSStatus.Error when the resolver files cannot be changed.
	// The system's own words go to the daemon log.
	dnsWriteText  = "RunOS could not write the private DNS settings for this cluster on this Mac"
	dnsRemoveText = "RunOS could not remove its private DNS settings from this Mac"
	// dnsHandAuthoredText is DNSStatus.Error when the person already has a resolver file for the zone.
	dnsHandAuthoredText = "A resolver file for this cluster's private DNS zone already exists on this Mac " +
		"and was not made by RunOS, so RunOS left it alone."
	// dnsUnreadableText is DNSStatus.Error when the Mac's own DNS view cannot be read.
	dnsUnreadableText = "RunOS could not read this Mac's DNS settings to check the private DNS"
)

// dnsCheck is one look at the Mac's DNS view against the zones the plan needs.
type dnsCheck struct {
	// Pending lists the zones macOS does not list at all yet, sorted.
	Pending []string
	// Overridden is set when another resolver answers for a zone. That is a real fault, not a delay.
	Overridden error
	// OverriddenZones lists the zones another resolver answers for, sorted.
	OverriddenZones []string
}

// OK reports whether every zone is served by the tunnel resolver.
func (c dnsCheck) OK() bool { return len(c.Pending) == 0 && c.Overridden == nil }

// checkEffectiveResolvers compares what macOS uses (parsed `scutil --dns`) with the plan.
//
// A zone macOS does not list is PENDING: writing /etc/resolver/<zone> does not take effect the
// instant the file lands, because configd republishes its DNS configuration on its own schedule.
// A zone macOS lists with a DIFFERENT resolver is a fault, and no waiting fixes it.
//
// Measured on a developer Mac: the same connect failed on a single look and worked moments later with
// the file already on disk and the zone listed. On another run the zone stayed unlisted for minutes,
// so this check never decides alone that a pending zone is lost; the daemon's watcher does, after a
// bound (dns_schedule.go).
func checkEffectiveResolvers(effective map[string][]netip.Addr, expected []ResolverPlan) dnsCheck {
	var c dnsCheck
	for _, plan := range expected {
		servers := effective[plan.Zone]
		if len(servers) == 0 {
			c.Pending = append(c.Pending, plan.Zone)
			continue
		}
		for _, server := range servers {
			if server != plan.Resolver {
				if c.Overridden == nil {
					c.Overridden = fmt.Errorf("private DNS zone %s is overridden by resolver %s", plan.Zone, server)
				}
				c.OverriddenZones = append(c.OverriddenZones, plan.Zone)
				break
			}
		}
	}
	sort.Strings(c.Pending)
	sort.Strings(c.OverriddenZones)
	return c
}

// verifyEffectiveResolvers is the one-shot check Windows keeps: its NRPT policy applies when the
// rule is added, so an unlisted zone there is a fault and not a delay.
func verifyEffectiveResolvers(effective map[string][]netip.Addr, expected []ResolverPlan) error {
	check := checkEffectiveResolvers(effective, expected)
	if check.Overridden != nil {
		return check.Overridden
	}
	if len(check.Pending) > 0 {
		return fmt.Errorf("private DNS zone %s is not effective yet", check.Pending[0])
	}
	return nil
}
