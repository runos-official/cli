package vpn

import "net/netip"

func resolvedDNSCommands(iface string, endpoint netip.AddrPort, resolvers []ResolverPlan) [][]string {
	domains := []string{"domain", iface}
	for _, resolver := range resolvers {
		domains = append(domains, "~"+resolver.Zone)
	}
	return [][]string{
		{"dns", iface, resolvedDNSServer(endpoint)},
		domains,
		{"default-route", iface, "no"},
	}
}

func resolvedDNSServer(endpoint netip.AddrPort) string {
	if endpoint.Port() == 53 {
		return endpoint.Addr().String()
	}
	return endpoint.String()
}

func resolvedDNSProbeCommand(iface, zone string) []string {
	return []string{"--interface=" + iface, "query", zone}
}
