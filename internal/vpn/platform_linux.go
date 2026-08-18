//go:build linux

package vpn

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
)

// The Linux platform uses a local router for private DNS. systemd-resolved sends only configured
// zones to that router. The router sends each zone only to its assigned cluster resolver.

// defaultTunName is the interface the engine creates on Linux.
const defaultTunName = "runos0"

type linuxPlatform struct {
	resolved bool
	router   *dnsRouter
}

func newPlatform() platform {
	return &linuxPlatform{resolved: resolvectlAvailable()}
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

func (p *linuxPlatform) ReconcileDNS(iface string, clientAddr netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	if !p.resolved {
		p.stopRouter()
		return DNSStatus{
			Mode:  "unavailable",
			Error: "systemd-resolved is unavailable; private names can resolve publicly",
		}, nil
	}
	if len(resolvers) == 0 {
		if out, err := run("resolvectl", "revert", iface); err != nil {
			return DNSStatus{Mode: "unavailable", Error: "remove private DNS state"}, fmt.Errorf("revert link DNS: %w: %s", err, out)
		}
		p.stopRouter()
		_, _ = run("resolvectl", "flush-caches")
		return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
	}
	if !clientAddr.IsValid() {
		return DNSStatus{Mode: "unavailable", Error: "the VPN client address is unavailable"}, fmt.Errorf("configure private DNS without a client address")
	}
	routes := dnsRoutesForPlans(resolvers)
	if p.router != nil {
		p.router.Update(routes)
		if err := applyAndVerifyResolvedDNS(iface, p.router.Addr(), resolvers); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
		return DNSStatus{Available: true, Mode: "local-proxy"}, nil
	}

	loopback, err := startDNSRouter(netip.MustParseAddrPort("127.0.0.1:0"), routes, defaultDNSUpstreamDeadline)
	if err == nil {
		if applyErr := applyAndVerifyResolvedDNS(iface, loopback.Addr(), resolvers); applyErr == nil {
			p.router = loopback
			return DNSStatus{Available: true, Mode: "local-proxy"}, nil
		}
		loopback.Close()
		_, _ = run("resolvectl", "revert", iface)
	}

	fallbackAddr := netip.AddrPortFrom(clientAddr, 53)
	fallback, err := startDNSRouter(fallbackAddr, routes, defaultDNSUpstreamDeadline)
	if err != nil {
		state := DNSStatus{Mode: "unavailable", Error: "cannot start the local DNS router"}
		return state, fmt.Errorf("start local DNS router: %w", err)
	}
	if err := applyAndVerifyResolvedDNS(iface, fallback.Addr(), resolvers); err != nil {
		fallback.Close()
		_, _ = run("resolvectl", "revert", iface)
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	p.router = fallback
	return DNSStatus{Available: true, Mode: "local-proxy"}, nil
}

func applyAndVerifyResolvedDNS(iface string, endpoint netip.AddrPort, resolvers []ResolverPlan) error {
	if err := applyResolvedDNS(iface, endpoint, resolvers); err != nil {
		return err
	}
	_, _ = run("resolvectl", "flush-caches")
	probe := resolvedDNSProbeCommand(iface, resolvers[0].Zone)
	if out, err := run("resolvectl", probe...); err != nil {
		return fmt.Errorf("verify private DNS for %s: %w: %s", resolvers[0].Zone, err, out)
	}
	return nil
}

func applyResolvedDNS(iface string, endpoint netip.AddrPort, resolvers []ResolverPlan) error {
	labels := []string{"set link DNS endpoint", "set link DNS domains", "disable the default DNS route"}
	for index, command := range resolvedDNSCommands(iface, endpoint, resolvers) {
		if out, err := run("resolvectl", command...); err != nil {
			return fmt.Errorf("%s: %w: %s", labels[index], err, out)
		}
	}
	return nil
}

func (p *linuxPlatform) stopRouter() {
	if p.router != nil {
		p.router.Close()
		p.router = nil
	}
}

func (p *linuxPlatform) Teardown(iface string) error {
	if p.resolved {
		_, _ = run("resolvectl", "revert", iface)
	}
	p.stopRouter()
	return nil
}
