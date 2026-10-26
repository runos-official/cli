package vpn

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

/*
What `runos vpn diagnose` says about private DNS on macOS (FCR 1730, H5).

The daemon can only report that macOS has not listed a zone. The person needs to know whether the
resolver file is there, whether it is whole, and whether macOS lists the zone at all, because those
point at different causes. Everything here is readable without root: /etc/resolver is world-readable
and `scutil --dns` needs no privilege.
*/

// ResolverFileFact is what the report saw about one zone's resolver file.
type ResolverFileFact struct {
	Present bool
	// Managed: the file carries RunOS's marker.
	Managed bool
	// Empty: the file has no content, which is what a write cut short leaves.
	Empty bool
	// Resolver is the nameserver the file names; invalid when it names none.
	Resolver netip.Addr
	// Age is the time since the file was last written; negative when unknown.
	Age time.Duration
}

// DNSProbe is the macOS DNS state diagnose collected.
type DNSProbe struct {
	// Zones are the zones the daemon's status says the connected clusters need.
	Zones []ResolverPlan
	Files map[string]ResolverFileFact
	// Effective is the zone -> resolvers listing from `scutil --dns`.
	Effective map[string][]netip.Addr
	// EffectiveErr is why the listing could not be read, when it could not.
	EffectiveErr string
}

// ZonesFromStatus lists the zones and resolvers of the clusters the device is connected to.
func ZonesFromStatus(st *Status) []ResolverPlan {
	if st == nil {
		return nil
	}
	var zones []ResolverPlan
	for _, c := range st.Clusters {
		if !c.Connected {
			continue
		}
		resolver, err := netip.ParseAddr(c.Resolver)
		if err != nil {
			continue
		}
		for _, zone := range c.Zones {
			zones = append(zones, ResolverPlan{Zone: strings.ToLower(zone), Resolver: resolver})
		}
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Zone < zones[j].Zone })
	return zones
}

// ProbeMacDNS reads the resolver files for the zones from dir and the macOS listing from scutil.
// The inputs are parameters so a test runs it on a temp directory.
func ProbeMacDNS(dir string, zones []ResolverPlan, scutil func() (string, error), now time.Time) DNSProbe {
	probe := DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}}
	for _, plan := range zones {
		path := filepath.Join(dir, plan.Zone)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fact := ResolverFileFact{Present: true, Age: -1}
		if info, err := os.Stat(path); err == nil {
			fact.Age = now.Sub(info.ModTime())
		}
		fact.Empty = len(bytes.TrimSpace(data)) == 0
		fact.Managed = isManagedResolverFile(data)
		if addr, ok := parseResolverNameserver(data); ok {
			fact.Resolver = addr
		}
		probe.Files[plan.Zone] = fact
	}
	out, err := scutil()
	if err != nil {
		probe.EffectiveErr = err.Error()
		return probe
	}
	probe.Effective = parseScutilDNS(out)
	return probe
}

// ProbeMacDNSNow collects the probe from this Mac. It needs no root.
func ProbeMacDNSNow(zones []ResolverPlan) DNSProbe {
	scutil := func() (string, error) {
		out, err := runBounded("scutil", "--dns")
		return string(out), err
	}
	return ProbeMacDNS(resolverDir, zones, scutil, time.Now())
}

const dnsNextStep = "Next step: turn the VPN off and on again. If it keeps happening, send this report to support."

// dnsFindings turns the probe into plain findings, one per zone that is not in order.
func dnsFindings(p DNSProbe) []string {
	var findings []string
	for _, plan := range p.Zones {
		file := p.Files[plan.Zone]
		switch {
		case !file.Present:
			findings = append(findings, fmt.Sprintf(
				"there is no resolver file for %s, so macOS cannot send that zone to the cluster. %s", plan.Zone, dnsNextStep))
		case file.Empty:
			findings = append(findings, fmt.Sprintf(
				"the resolver file for %s is empty (a write that was cut short). %s", plan.Zone, dnsNextStep))
		case !file.Managed:
			findings = append(findings, fmt.Sprintf(
				"there is a resolver file for %s that RunOS did not write, so RunOS left it alone and the zone is not sent to the cluster. "+
					"Next step: remove it if it is not yours, then turn the VPN off and on again.", plan.Zone))
		case file.Resolver.IsValid() && file.Resolver != plan.Resolver:
			findings = append(findings, fmt.Sprintf(
				"the resolver file for %s holds %s but the cluster resolver is %s. %s", plan.Zone, file.Resolver, plan.Resolver, dnsNextStep))
		default:
			if f := listingFinding(p, plan, file); f != "" {
				findings = append(findings, f)
			}
		}
	}
	if p.EffectiveErr != "" && len(p.Zones) > 0 {
		findings = append(findings, "diagnose could not read this Mac's DNS settings to check them: "+p.EffectiveErr)
	}
	return findings
}

// listingFinding compares a well-formed file with what macOS lists.
func listingFinding(p DNSProbe, plan ResolverPlan, file ResolverFileFact) string {
	if p.EffectiveErr != "" {
		return ""
	}
	servers := p.Effective[plan.Zone]
	if len(servers) == 0 {
		return fmt.Sprintf("the resolver file for %s is on disk but macOS does not list it%s. %s",
			plan.Zone, checkedAfter(file.Age), dnsNextStep)
	}
	for _, server := range servers {
		if server != plan.Resolver {
			return fmt.Sprintf("macOS sends %s to %s instead of the cluster resolver %s; another VPN or DNS tool may own that zone.",
				plan.Zone, server, plan.Resolver)
		}
	}
	return ""
}

// checkedAfter says how long after the write the check was made, or nothing when that is unknown.
func checkedAfter(age time.Duration) string {
	if age < 0 {
		return ""
	}
	seconds := int(age.Round(time.Second) / time.Second)
	switch {
	case seconds >= 120:
		return fmt.Sprintf(" (checked %d minutes after the write)", seconds/60)
	case seconds == 1:
		return " (checked 1 second after the write)"
	default:
		return fmt.Sprintf(" (checked %d seconds after the write)", seconds)
	}
}
