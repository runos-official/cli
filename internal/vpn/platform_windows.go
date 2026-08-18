//go:build windows

package vpn

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// The Windows platform. The interface is a wintun adapter wireguard-go creates (wintun.dll beside
// runos.exe); its address and routes go through `netsh interface ipv4`, and split DNS is a Name
// Resolution Policy Table rule per zone (`Add-DnsClientNrptRule`), which is how Windows steers one
// namespace to one resolver without touching the machine's adapters. Every rule the daemon writes
// carries the comment `runos-vpn`, so Resolvers/Teardown see exactly the daemon's rules and never
// a hand-written or group-policy one.
//
// netsh and PowerShell are shelled out to rather than the IP helper and WMI APIs: both are on
// every supported Windows, and their words in an error are what a person can act on.

const (
	// defaultTunName is the adapter name wireguard-go requests; it is also what netsh calls it.
	defaultTunName = "runos"
	nrptComment    = "runos-vpn"
)

type windowsPlatform struct{}

func newPlatform() platform { return windowsPlatform{} }

func (windowsPlatform) SetInterfaceAddress(iface string, addr netip.Prefix) error {
	// The adapter can take a moment to register with the IP stack after wintun creates it, and
	// netsh answers "The system cannot find the file specified" until it has; retry briefly.
	var out []byte
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		out, err = run("netsh", "interface", "ipv4", "set", "address",
			"name="+iface, "source=static", "addr="+addr.Addr().String(), "mask=255.255.255.255")
		if err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("set interface address: %w: %s", err, out)
}

func (windowsPlatform) Routes(string) ([]netip.Prefix, error) {
	// Write-only-diffable, as on the other platforms: AddRoute tolerates an existing route.
	return nil, nil
}

func (windowsPlatform) AddRoute(iface string, prefix netip.Prefix) error {
	out, err := run("netsh", "interface", "ipv4", "add", "route", prefix.String(), iface, "store=active")
	if err != nil {
		if strings.Contains(string(out), "already exists") {
			return nil
		}
		return fmt.Errorf("add route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (windowsPlatform) RemoveRoute(iface string, prefix netip.Prefix) error {
	out, err := run("netsh", "interface", "ipv4", "delete", "route", prefix.String(), iface)
	if err != nil {
		if strings.Contains(string(out), "not found") {
			return nil
		}
		return fmt.Errorf("remove route %s: %w: %s", prefix, err, out)
	}
	return nil
}

func (windowsPlatform) Resolvers() (map[string]netip.Addr, error) {
	out, err := powershell(`Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '` + nrptComment + `' } | ForEach-Object { ($_.Namespace -join ',') + '|' + ($_.NameServers -join ',') }`)
	if err != nil {
		return nil, fmt.Errorf("read NRPT rules: %w: %s", err, out)
	}
	return parseNrptRules(string(out)), nil
}

func (p windowsPlatform) SetResolver(zone string, resolver netip.Addr) error {
	// Replace rather than update: an NRPT rule is addressed by an opaque name, so the simplest
	// exact converge is remove-ours-for-this-zone then add. Both the dotted form (subdomains) and
	// the bare zone (the apex itself) are named, because a leading dot matches only below it.
	if err := p.RemoveResolver(zone); err != nil {
		return err
	}
	out, err := powershell(fmt.Sprintf(`Add-DnsClientNrptRule -Namespace '.%s','%s' -NameServers '%s' -Comment '%s'`,
		zone, zone, resolver.String(), nrptComment))
	if err != nil {
		return fmt.Errorf("add NRPT rule for %s: %w: %s", zone, err, out)
	}
	return nil
}

func (windowsPlatform) RemoveResolver(zone string) error {
	out, err := powershell(fmt.Sprintf(`Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '%s' -and (($_.Namespace -contains '.%s') -or ($_.Namespace -contains '%s')) } | Remove-DnsClientNrptRule -Force`,
		nrptComment, zone, zone))
	if err != nil {
		return fmt.Errorf("remove NRPT rule for %s: %w: %s", zone, err, out)
	}
	return nil
}

func (p windowsPlatform) ReconcileDNS(_ string, _ netip.Addr, resolvers []ResolverPlan) (DNSStatus, error) {
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
	out, err := powershell(`Get-DnsClientNrptPolicy -Effective | ForEach-Object { ($_.Namespace -join ',') + '|' + ($_.NameServers -join ',') }`)
	if err != nil {
		err = fmt.Errorf("read effective NRPT policy: %w: %s", err, out)
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	if err := verifyEffectiveResolvers(parseEffectiveNrptPolicy(string(out)), resolvers); err != nil {
		return DNSStatus{Mode: "unavailable", Error: err.Error()}, err
	}
	return DNSStatus{Available: true, Mode: "native"}, nil
}

func (windowsPlatform) FlushDNS() error {
	_, _ = run("ipconfig", "/flushdns")
	return nil
}

func (p windowsPlatform) Teardown(string) error {
	out, err := powershell(`Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '` + nrptComment + `' } | Remove-DnsClientNrptRule -Force`)
	if err != nil {
		return fmt.Errorf("remove NRPT rules: %w: %s", err, out)
	}
	_ = p.FlushDNS()
	// Routes go with the adapter when the engine closes it.
	return nil
}

func powershell(command string) ([]byte, error) {
	return run("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
}
