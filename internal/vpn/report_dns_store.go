package vpn

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// What `runos vpn diagnose` reads from the dynamic store (FCR 1730). `scutil` list and show need no
// root, so diagnose reads the store as the person who runs it and never writes to it.

// StoreFact is what the dynamic store holds for RunOS.
type StoreFact struct {
	// Checked is true when the store was read. False with ReadErr set means it could not be read, and
	// false with no ReadErr means it was not collected.
	Checked bool
	ReadErr string
	// Zones maps each zone in one of our DNS entries to the resolver that entry names.
	Zones map[string]netip.Addr
}

// ProbeMacStore reads our DNS entries from the store through run, which feeds a script to scutil.
func ProbeMacStore(run func(script string) (string, error)) StoreFact {
	fact := StoreFact{Zones: map[string]netip.Addr{}}
	out, err := run(storeListScript())
	if err != nil {
		fact.ReadErr = err.Error()
		return fact
	}
	var ids []string
	seen := map[string]bool{}
	for _, key := range parseStoreKeys(out) {
		if id := storeServiceOf(key); strings.HasSuffix(key, "/DNS") && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		out, err := run("show " + dnsStoreKeyRoot + id + "/DNS\n")
		if err != nil {
			fact.ReadErr = err.Error()
			return fact
		}
		if entry, ok := parseStoreDNS(out); ok && len(entry.Servers) > 0 {
			for _, zone := range entry.Zones {
				fact.Zones[zone] = entry.Servers[0]
			}
		}
	}
	fact.Checked = true
	return fact
}

// zoneMechanisms is what diagnose saw for one zone.
type zoneMechanisms struct{ File, Store, Listed bool }

func (p DNSProbe) mechanisms(plan ResolverPlan) zoneMechanisms {
	file := p.Files[plan.Zone]
	m := zoneMechanisms{
		File:  file.Present && file.Managed && !file.Empty && (!file.Resolver.IsValid() || file.Resolver == plan.Resolver),
		Store: p.Store.Zones[plan.Zone] == plan.Resolver,
	}
	servers := p.Effective[plan.Zone]
	m.Listed = len(servers) > 0
	for _, server := range servers {
		if server != plan.Resolver {
			m.Listed = false
		}
	}
	return m
}

// storeFinding is the finding for one zone when the store was read. handled is false when the file
// logic alone should speak: the store was not read, or only the file is in place.
func storeFinding(p DNSProbe, plan ResolverPlan, file ResolverFileFact) (finding string, handled bool) {
	if !p.Store.Checked {
		return "", false
	}
	if p.EffectiveErr != "" {
		return "", true // the one finding about the unreadable listing is added by dnsFindings
	}
	for _, server := range p.Effective[plan.Zone] {
		if server != plan.Resolver {
			return listingFinding(p, plan, file), true
		}
	}
	m := p.mechanisms(plan)
	switch {
	case m.Listed:
		return "", true
	case m.Store && m.File:
		return fmt.Sprintf("the dynamic-store entry and the resolver file for %s are in place but macOS does not list it%s. %s",
			plan.Zone, checkedAfter(file.Age), dnsNextStep), true
	case m.Store:
		return fmt.Sprintf("the dynamic-store entry for %s is in place (no resolver file) but macOS does not list it. %s",
			plan.Zone, dnsNextStep), true
	case !file.Present:
		return fmt.Sprintf("RunOS has not set up private DNS for %s on this Mac (no dynamic-store entry and no resolver file), "+
			"so macOS cannot send that zone to the cluster. %s", plan.Zone, dnsNextStep), true
	}
	return "", false
}

// dnsMechanismLines says, per zone, which of the three things are in place. A thing diagnose could
// not read says "unknown", never "no".
func dnsMechanismLines(p DNSProbe) []string {
	lines := make([]string, 0, len(p.Zones))
	for _, plan := range p.Zones {
		m := p.mechanisms(plan)
		store := yesNo(m.Store)
		if !p.Store.Checked {
			store = "unknown"
		}
		listed := yesNo(m.Listed)
		if p.EffectiveErr != "" {
			listed = "unknown"
		}
		lines = append(lines, fmt.Sprintf("private DNS for %s: resolver file %s, dynamic-store entry %s, listed by macOS %s",
			plan.Zone, yesNo(m.File), store, listed))
	}
	return lines
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
