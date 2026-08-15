package vpn

import (
	"net/netip"
	"strings"
)

// Pure parsing for the Windows NRPT read-back, kept out of the Windows build tag so the Mac test
// run covers it. Each line is one rule the daemon owns: `namespaces|servers`, both comma-joined,
// e.g. `.wm2.example,wm2.example|172.24.32.1`.

// parseNrptRules maps every zone named by a rule (dotted or bare, lower-cased, without the
// leading dot) to the rule's first name server. Lines that carry no parsable server are skipped.
func parseNrptRules(out string) map[string]netip.Addr {
	found := map[string]netip.Addr{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		namespaces, servers, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		first := strings.TrimSpace(strings.Split(servers, ",")[0])
		resolver, err := netip.ParseAddr(first)
		if err != nil {
			continue
		}
		for _, ns := range strings.Split(namespaces, ",") {
			zone := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ns), "."))
			if zone != "" {
				found[zone] = resolver
			}
		}
	}
	return found
}
