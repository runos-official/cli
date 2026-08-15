//go:build linux

package vpn

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// The Linux platform. The interface is addressed and routed with `ip` (iproute2, present on every
// modern distro); split DNS is systemd-resolved via `resolvectl` when it is running, steering only
// the cluster's zones to the tunnel resolver with a routing domain (`~zone`). When resolved is not
// present the daemon does NOT rewrite /etc/resolv.conf (that is a machine-wide file other things
// own); it reports split DNS as unavailable and the tunnel still carries traffic, names just
// resolve publicly.
//
// `resolvectl domain LINK ...` sets the link's WHOLE domain list (measured: a second call with a
// new zone replaced the first), so the per-zone Set/Remove below read the link's current list
// back and write the full list every time. Resolvers() parses the same read-back, so the daemon's
// diff sees what resolved really holds.

// defaultTunName is the interface the engine creates on Linux.
const defaultTunName = "runos0"

type linuxPlatform struct {
	// resolved is true when systemd-resolved answers, decided once at construction.
	resolved bool
}

func newPlatform() platform {
	return linuxPlatform{resolved: resolvectlAvailable()}
}

func resolvectlAvailable() bool {
	return exec.Command("resolvectl", "status").Run() == nil
}

func (linuxPlatform) SetInterfaceAddress(iface string, addr netip.Prefix) error {
	// A /32 on the interface plus per-cluster routes is the same shape as macOS. `ip addr replace`
	// is idempotent, and the link is brought up explicitly.
	if out, err := run("ip", "address", "replace", addr.Addr().String()+"/32", "dev", iface); err != nil {
		return fmt.Errorf("set interface address: %w: %s", err, out)
	}
	if out, err := run("ip", "link", "set", "dev", iface, "up"); err != nil {
		return fmt.Errorf("bring interface up: %w: %s", err, out)
	}
	return nil
}

func (linuxPlatform) Routes(string) ([]netip.Prefix, error) {
	// As on macOS, routes are diffed write-only: AddRoute is idempotent (`ip route replace`), so
	// returning an empty current set adds whatever the plan wants and RemoveRoute handles drops.
	return nil, nil
}

func (linuxPlatform) AddRoute(iface string, prefix netip.Prefix) error {
	if out, err := run("ip", "route", "replace", prefix.String(), "dev", iface); err != nil {
		return fmt.Errorf("add route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (linuxPlatform) RemoveRoute(iface string, prefix netip.Prefix) error {
	if out, err := run("ip", "route", "del", prefix.String(), "dev", iface); err != nil {
		if strings.Contains(string(out), "No such process") {
			return nil
		}
		return fmt.Errorf("remove route %s: %w: %s", prefix, err, out)
	}
	return nil
}

// Resolvers reads the link's routing domains and DNS server back from resolved. Every routing
// domain on the link maps to the link's (single) resolver: the daemon steers all of a link's
// zones to one tunnel resolver per cluster, and two clusters on one link share the DNS server
// list, so the first server is what every zone gets.
func (p linuxPlatform) Resolvers() (map[string]netip.Addr, error) {
	found := map[string]netip.Addr{}
	if !p.resolved {
		return found, nil
	}
	dnsOut, err := run("resolvectl", "dns", defaultTunName)
	if err != nil {
		return found, nil // link not known to resolved yet: nothing steered
	}
	servers := parseResolvectlLinkValues(string(dnsOut))
	if len(servers) == 0 {
		return found, nil
	}
	resolver, err := netip.ParseAddr(servers[0])
	if err != nil {
		return found, nil
	}
	domOut, err := run("resolvectl", "domain", defaultTunName)
	if err != nil {
		return found, nil
	}
	for _, zone := range routingZones(parseResolvectlLinkValues(string(domOut))) {
		found[zone] = resolver
	}
	return found, nil
}

func (p linuxPlatform) SetResolver(zone string, resolver netip.Addr) error {
	if !p.resolved {
		// No resolved: do not touch resolv.conf. The tunnel works; the name resolves publicly.
		// Returning nil keeps the daemon converging routes even without split DNS.
		return nil
	}
	if out, err := run("resolvectl", "dns", defaultTunName, resolver.String()); err != nil {
		return fmt.Errorf("set link dns: %w: %s", err, out)
	}
	return p.setDomains(addZone(p.currentZones(), zone))
}

func (p linuxPlatform) RemoveResolver(zone string) error {
	if !p.resolved {
		return nil
	}
	return p.setDomains(removeZone(p.currentZones(), zone))
}

func (p linuxPlatform) currentZones() []string {
	out, err := run("resolvectl", "domain", defaultTunName)
	if err != nil {
		return nil
	}
	return routingZones(parseResolvectlLinkValues(string(out)))
}

// setDomains writes the link's whole routing-domain list. An empty list clears it (resolvectl
// wants one empty argument for that).
func (p linuxPlatform) setDomains(zones []string) error {
	args := []string{"domain", defaultTunName}
	if len(zones) == 0 {
		args = append(args, "")
	}
	for _, zone := range zones {
		args = append(args, "~"+zone)
	}
	if out, err := run("resolvectl", args...); err != nil {
		return fmt.Errorf("set link domains: %w: %s", err, out)
	}
	return nil
}

func (linuxPlatform) FlushDNS() error {
	_, _ = run("resolvectl", "flush-caches")
	return nil
}

func (p linuxPlatform) Teardown(iface string) error {
	if p.resolved {
		// Revert the link's DNS so no zone is steered after the tunnel goes down.
		_, _ = run("resolvectl", "revert", iface)
	}
	// Routes go with the interface when the engine closes it.
	return nil
}
