package vpn

import (
	"sort"
	"strings"
)

// Pure parsing for systemd-resolved's `resolvectl dns|domain LINK` output, kept out of the Linux
// build tag so the Mac test run covers it. One line per link: `Link 6 (wg0): value value ...`,
// and a bare `Link 6 (wg0):` when the link holds nothing.

// parseResolvectlLinkValues returns the whitespace-separated values after the first `Link N
// (name):` prefix, or nothing when the line carries none.
func parseResolvectlLinkValues(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Link ") {
			continue
		}
		_, rest, ok := strings.Cut(line, "):")
		if !ok {
			continue
		}
		if fields := strings.Fields(rest); len(fields) > 0 {
			return fields
		}
		return nil
	}
	return nil
}

// routingZones keeps only routing domains (`~zone`) from a resolvectl domain list, stripped of
// the tilde and lower-cased. A search domain without the tilde is not the daemon's and is left
// alone.
func routingZones(values []string) []string {
	var zones []string
	for _, v := range values {
		if strings.HasPrefix(v, "~") && len(v) > 1 {
			zones = append(zones, strings.ToLower(strings.TrimSuffix(v[1:], ".")))
		}
	}
	return zones
}

// addZone returns zones plus zone, sorted and without duplicates.
func addZone(zones []string, zone string) []string {
	set := map[string]struct{}{zone: {}}
	for _, z := range zones {
		set[z] = struct{}{}
	}
	return sortedZones(set)
}

// removeZone returns zones without zone, sorted.
func removeZone(zones []string, zone string) []string {
	set := map[string]struct{}{}
	for _, z := range zones {
		if z != zone {
			set[z] = struct{}{}
		}
	}
	return sortedZones(set)
}

func sortedZones(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for z := range set {
		out = append(out, z)
	}
	sort.Strings(out)
	return out
}
