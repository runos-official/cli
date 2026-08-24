//go:build darwin

package vpn

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The macOS platform. Routes go through `route` against the utun interface; split DNS is
// `/etc/resolver/<zone>` files, each one line `nameserver <resolver>`, which is how macOS steers a
// single domain to a resolver without touching the machine's default DNS. `dig` bypasses this
// (it talks to the resolver directly), so the proof uses the system path: `dscacheutil -q host`
// or `curl`.
//
// Every resolver file the daemon writes carries a marker comment so Teardown removes exactly the
// daemon's files and never a hand-written one.

const (
	resolverDir    = "/etc/resolver"
	resolverMarker = "# managed by runos-vpn"
)

// defaultTunName is the interface the engine requests on macOS; the kernel appends the number.
const defaultTunName = "utun"

type darwinPlatform struct{}

func newPlatform() platform { return darwinPlatform{} }

func (darwinPlatform) SetInterfaceAddress(iface string, addr netip.Prefix) error {
	ip := addr.Addr().String()
	// A utun is point-to-point; macOS `ifconfig utunN <ip> <ip>` sets both ends to the device's
	// own address, which is what a /32 client wants.
	if out, err := run("ifconfig", iface, "inet", ip, ip, "up"); err != nil {
		return fmt.Errorf("set interface address: %w: %s", err, out)
	}
	return nil
}

func (darwinPlatform) Routes(iface string) ([]netip.Prefix, error) {
	// `netstat -rn -f inet` lists the routing table; the daemon's routes are those whose gateway
	// or interface column is the utun. Parsing netstat is brittle, so the daemon instead treats
	// routes as write-only-diffable: it asks for none back and relies on AddRoute being
	// idempotent (route add of an existing route is a no-op the daemon tolerates). Returning an
	// empty set means "add whatever the plan wants"; RemoveRoute on teardown covers the rest.
	return nil, nil
}

func (darwinPlatform) AddRoute(iface string, prefix netip.Prefix) error {
	// -ifscope pins the route to the interface, and `route add` replaces an existing route rather
	// than failing, so this is idempotent.
	if out, err := run("route", "-q", "-n", "add", "-net", prefix.String(), "-interface", iface); err != nil {
		if strings.Contains(string(out), "File exists") {
			return nil
		}
		return fmt.Errorf("add route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (darwinPlatform) RemoveRoute(iface string, prefix netip.Prefix) error {
	if out, err := run("route", "-q", "-n", "delete", "-net", prefix.String(), "-interface", iface); err != nil {
		if strings.Contains(string(out), "not in table") {
			return nil
		}
		return fmt.Errorf("remove route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (darwinPlatform) Resolvers() (map[string]netip.Addr, error) {
	found := map[string]netip.Addr{}
	entries, err := os.ReadDir(resolverDir)
	if err != nil {
		if os.IsNotExist(err) {
			return found, nil
		}
		return nil, fmt.Errorf("read %s: %w", resolverDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(resolverDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(resolverMarker)) {
			continue // not ours
		}
		if addr, ok := parseResolverNameserver(data); ok {
			found[entry.Name()] = addr
		}
	}
	return found, nil
}

func (darwinPlatform) SetResolver(zone string, resolver netip.Addr) error {
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", resolverDir, err)
	}
	content := fmt.Sprintf("%s\nnameserver %s\n", resolverMarker, resolver.String())
	if err := os.WriteFile(filepath.Join(resolverDir, zone), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write resolver for %s: %w", zone, err)
	}
	return nil
}

func (darwinPlatform) RemoveResolver(zone string) error {
	path := filepath.Join(resolverDir, zone)
	// Only remove a file the daemon wrote, never a hand-authored /etc/resolver entry.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !bytes.Contains(data, []byte(resolverMarker)) {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove resolver for %s: %w", zone, err)
	}
	return nil
}

func (p darwinPlatform) ReconcileDNS(_ string, _ netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
	have, err := p.Resolvers()
	if err != nil {
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	diff := DiffResolvers(have, resolvers)
	for _, resolver := range diff.Set {
		if err := p.SetResolver(resolver.Zone, resolver.Resolver); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	for _, zone := range diff.Remove {
		if err := p.RemoveResolver(zone); err != nil {
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
	}
	if len(diff.Set) > 0 || len(diff.Remove) > 0 {
		_ = p.FlushDNS()
	}
	if len(resolvers) == 0 {
		return DNSStatus{Mode: "unavailable", Error: "no private DNS zones are active"}, nil
	}
	// POLL, DO NOT SNAPSHOT. Writing /etc/resolver/<zone> does not take effect the instant the file
	// lands: macOS republishes its DNS configuration asynchronously. Reading `scutil --dns` once,
	// immediately after the write, is a coin flip.
	//
	// MEASURED 2026-08-20: `runos vpn connect cl1` failed with "private DNS zone ... is not
	// effective" and the SAME command, unchanged, succeeded moments later, with the resolver file
	// already on disk and scutil already listing the zone as Reachable. Nothing was wrong with the
	// cluster; the check was early. A first connect that reads as a broken cluster is the kind of
	// failure people stop trusting the tool over.
	//
	// Bounded, because a zone that never appears is a real fault and must still be reported.
	var lastErr error
	deadline := time.Now().Add(dnsEffectiveTimeout)
	for {
		out, err := run("scutil", "--dns")
		if err != nil {
			err = fmt.Errorf("read effective macOS DNS: %w: %s", err, out)
			return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
		}
		lastErr = verifyEffectiveResolvers(parseScutilDNS(string(out)), resolvers)
		if lastErr == nil {
			return DNSStatus{Available: true, Mode: "native"}, nil
		}
		if time.Now().After(deadline) {
			return DNSStatus{Mode: "unavailable", Error: lastErr.Error()}, lastErr
		}
		time.Sleep(dnsEffectivePoll)
	}
}

// How long to wait for macOS to republish its DNS configuration after a resolver write, and how
// often to re-read it. Five seconds is generous against the sub-second propagation measured, and
// short enough that a genuinely absent zone still reports promptly.
const (
	dnsEffectiveTimeout = 5 * time.Second
	dnsEffectivePoll    = 250 * time.Millisecond
)

func (darwinPlatform) FlushDNS() error {
	// Both are needed on modern macOS: dscacheutil clears the cache, mDNSResponder reloads it.
	_, _ = run("dscacheutil", "-flushcache")
	_, _ = run("killall", "-HUP", "mDNSResponder")
	return nil
}

func (p darwinPlatform) Teardown(iface string) error {
	resolvers, err := p.Resolvers()
	if err != nil {
		return err
	}
	for zone := range resolvers {
		if rmErr := p.RemoveResolver(zone); rmErr != nil {
			err = rmErr
		}
	}
	_ = p.FlushDNS()
	// The interface's routes go with the interface when the engine closes it, so there is nothing
	// to delete by prefix here; RemoveRoute is used for a single disconnected cluster while the
	// tunnel stays up.
	return err
}

func parseResolverNameserver(data []byte) (netip.Addr, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "nameserver" {
			if addr, err := netip.ParseAddr(fields[1]); err == nil {
				return addr, true
			}
		}
	}
	return netip.Addr{}, false
}
