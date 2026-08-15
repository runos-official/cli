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

// Resolvers returns nothing: resolvectl's per-link state is set wholesale by SetResolvers-style
// calls, so the daemon reconciles the whole link's DNS at once in setLinkDNS rather than diffing
// zone by zone. DiffResolvers on an empty current set therefore always re-applies the full plan,
// which resolvectl accepts idempotently.
func (linuxPlatform) Resolvers() (map[string]netip.Addr, error) {
	return map[string]netip.Addr{}, nil
}

func (p linuxPlatform) SetResolver(zone string, resolver netip.Addr) error {
	if !p.resolved {
		// No resolved: report once, do not touch resolv.conf. The tunnel works; the name resolves
		// publicly. Returning nil keeps the daemon converging routes even without split DNS.
		return nil
	}
	// resolvectl domain with a leading ~ is a ROUTING domain: only this zone goes to the link's
	// resolver, everything else is untouched. The resolver is set per-link too.
	if out, err := run("resolvectl", "dns", defaultTunName, resolver.String()); err != nil {
		return fmt.Errorf("set link dns: %w: %s", err, out)
	}
	if out, err := run("resolvectl", "domain", defaultTunName, "~"+zone); err != nil {
		return fmt.Errorf("set link domain %s: %w: %s", zone, err, out)
	}
	return nil
}

func (p linuxPlatform) RemoveResolver(zone string) error {
	// A per-link routing domain is cleared by resetting the link's domains; the daemon reapplies
	// the wanted set on the next converge, so a stale zone is dropped when the whole link is reset
	// in Teardown. Nothing to do per-zone here.
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
