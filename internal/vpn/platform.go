package vpn

import "net/netip"

// platform is the OS-specific half of the daemon: the interface address, the routes and the split
// DNS. The WireGuard layer (engine.go) is portable; this is not, because macOS uses `route` and
// `/etc/resolver`, Linux uses netlink and systemd-resolved, and Windows uses the IP helper API and
// NRPT. The daemon converges through this interface and never shells out itself.
//
// Every method is idempotent: the daemon computes a diff (plan.go) and calls only what changed,
// but a replayed call must be harmless, because sleep/wake and network changes make the daemon
// re-apply the whole plan.
type platform interface {
	// SetInterfaceAddress assigns the device's /32 to the interface and brings it up.
	SetInterfaceAddress(iface string, addr netip.Prefix) error
	// Routes returns the overlay routes currently pointing at the interface (masked prefixes), so
	// the daemon can diff them against the plan. Routes the daemon did not add are ignored.
	Routes(iface string) ([]netip.Prefix, error)
	AddRoute(iface string, prefix netip.Prefix) error
	RemoveRoute(iface string, prefix netip.Prefix) error
	// Resolvers returns the split-DNS zones the daemon currently steers, zone -> resolver.
	Resolvers() (map[string]netip.Addr, error)
	SetResolver(zone string, resolver netip.Addr) error
	RemoveResolver(zone string) error
	// FlushDNS drops the OS resolver cache after resolver changes, so a name resolves the new way
	// at once rather than after its old TTL.
	FlushDNS() error
	// Teardown removes every route and resolver the daemon owns, for `down`. The interface itself
	// goes away when the engine closes it.
	Teardown(iface string) error
}
