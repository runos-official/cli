package vpn

import (
	"net/netip"
	"strings"
)

// parseScutilDNS returns scoped resolver domains and their name servers.
func parseScutilDNS(output string) map[string][]netip.Addr {
	result := map[string][]netip.Addr{}
	var domain string
	var servers []netip.Addr
	flush := func() {
		if domain != "" && len(servers) > 0 {
			result[domain] = append(result[domain], servers...)
		}
		domain = ""
		servers = nil
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "resolver #") {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		switch {
		case key == "domain":
			domain = strings.TrimSuffix(strings.ToLower(value), ".")
		case strings.HasPrefix(key, "nameserver["):
			if server, err := netip.ParseAddr(value); err == nil {
				servers = append(servers, server)
			}
		}
	}
	flush()
	return result
}
