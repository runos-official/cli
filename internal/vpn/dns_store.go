package vpn

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Kept out of the darwin build tag, like managed_marker.go, so the Linux and Windows runs compile and
// test the same script writer and parsers.

/*
The macOS dynamic-store half of private DNS (FCR 1730).

WHAT IT IS. configd keeps the live network configuration in the SystemConfiguration dynamic store.
A key State:/Network/Service/<id>/DNS holding SupplementalMatchDomains and ServerAddresses is how
VPN clients (vpnc-script-macos, openconnect, Tunnelblick) tell macOS "send these domains to this
server". configd turns the entry into a resolver block that `scutil --dns` lists, flagged
Supplemental. /etc/resolver/<zone> reaches the same list through a different door, a directory
watch, which was measured to be slow or silent on macOS 26 (the zone stayed unlisted for minutes).

THE KEYS (names from SCSchemaDefinitions.h in the macOS SDK):

  Addresses, SubnetMasks, InterfaceName  the service's IPv4 entry. configd merges the DNS of a
      service it can see as a live interface; an orphan DNS key is the shape that gets ignored. The
      address is the device's own /32 and the interface is the utun, so the entry says "this service
      lives on utunN". There is deliberately NO Router and NO DestAddresses: without a router the
      service can never be elected primary, so the machine's default route and default DNS stay
      exactly as they were. This is a theory from the SDK's schema and the vpnc-script-macos
      convention, not something measured here.
  ServerAddresses                        the resolver (the cluster's private DNS address).
  SupplementalMatchDomains               the zones. A supplemental resolver is consulted only for
      names under these domains, so nothing else on the Mac changes how it resolves. SearchDomains is
      NOT set: the zones must not join the person's search list.
  SupplementalMatchOrders                deliberately NOT set. configd numbers supplemental resolvers
      itself, and the private zones are cluster domains no other software claims. (The key is
      plural, an array of numbers parallel to the domains; "SupplementalMatchOrder" is not a key.)

ONE SERVICE PER RESOLVER. A DNS dictionary has one ServerAddresses list shared by every match
domain in it. Clusters have different resolvers, so each distinct resolver is its own service,
named com.runos.vpn.<resolver>. Zones of the same resolver share the entry.

OWNERSHIP. Every key the daemon writes lives under State:/Network/Service/com.runos.vpn[.<slug>]/
and nothing else. Removal only ever names keys that storeOwnKey accepts, which is a strict match on
that shape, so a look-alike (com.runos.vpnx), another vendor's key or a UUID service can never be
named in a remove command.

WHY A READ-BACK. scutil reads its commands from stdin and its exit status does not say whether a
set was refused ("Permission denied" is printed, not returned). The caller reads the entry back with
show and trusts that, never the status of the write.
*/

const (
	// dnsStoreServiceID is the base service id. A service for a given resolver appends ".<slug>".
	dnsStoreServiceID = "com.runos.vpn"
	dnsStoreKeyRoot   = "State:/Network/Service/"
	// dnsStoreListPattern matches every key under our service ids and a few look-alikes. The brackets
	// avoid a backslash that scutil's own argument parsing could eat. parseStoreKeys is the real filter.
	dnsStoreListPattern = "State:/Network/Service/com[.]runos[.]vpn"
)

var (
	// storeOwnKey is the exact shape of a key this daemon writes.
	storeOwnKey = regexp.MustCompile(`^State:/Network/Service/com\.runos\.vpn(?:\.[a-z0-9_-]+)?/(DNS|IPv4)$`)
	// storeZoneName is a plain host name: letters, digits, hyphen and underscore, dot-separated. It
	// excludes anything scutil would read as another word or another command.
	storeZoneName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9_-]*[a-z0-9])?)*$`)
	// storeIfaceName is a BSD interface name such as utun9.
	storeIfaceName = regexp.MustCompile(`^[a-z]+[0-9]+$`)
	storeSlug      = strings.NewReplacer(".", "-", ":", "-")
)

// storeService is one dynamic-store service: a resolver and the zones it answers.
type storeService struct {
	ID       string
	Resolver netip.Addr
	// Zones are lower-case, without a trailing dot, sorted.
	Zones []string
}

func (s storeService) dnsKey() string  { return dnsStoreKeyRoot + s.ID + "/DNS" }
func (s storeService) ipv4Key() string { return dnsStoreKeyRoot + s.ID + "/IPv4" }

// dnsStoreServices groups the plan's zones by resolver, validates every zone name and returns the
// services sorted by id. A zone that is not a plain host name is an error, so no script is built
// from it.
func dnsStoreServices(resolvers []ResolverPlan) ([]storeService, error) {
	byResolver := map[netip.Addr]map[string]bool{}
	for _, plan := range resolvers {
		zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(plan.Zone)), ".")
		if !storeZoneName.MatchString(zone) {
			return nil, fmt.Errorf("private DNS zone %q is not a plain host name", plan.Zone)
		}
		if !plan.Resolver.IsValid() {
			return nil, fmt.Errorf("private DNS zone %s has no resolver", zone)
		}
		if byResolver[plan.Resolver] == nil {
			byResolver[plan.Resolver] = map[string]bool{}
		}
		byResolver[plan.Resolver][zone] = true
	}
	services := make([]storeService, 0, len(byResolver))
	for resolver, zones := range byResolver {
		svc := storeService{ID: dnsStoreServiceID + "." + storeSlug.Replace(resolver.String()), Resolver: resolver}
		for zone := range zones {
			svc.Zones = append(svc.Zones, zone)
		}
		sort.Strings(svc.Zones)
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	return services, nil
}

// storeApplyScript is the text fed to scutil to publish one service: the IPv4 entry first, then the
// DNS entry. d.init before each makes the script a replace, never an append, so a second run leaves
// one set of keys. The IPv4 entry is left out when the interface or the address is unknown.
func storeApplyScript(svc storeService, iface string, client netip.Addr) string {
	var b strings.Builder
	if storeIfaceName.MatchString(iface) && client.IsValid() {
		b.WriteString("d.init\n")
		fmt.Fprintf(&b, "d.add Addresses * %s\n", client)
		b.WriteString("d.add SubnetMasks * 255.255.255.255\n")
		fmt.Fprintf(&b, "d.add InterfaceName %s\n", iface)
		fmt.Fprintf(&b, "set %s\n", svc.ipv4Key())
	}
	b.WriteString("d.init\n")
	fmt.Fprintf(&b, "d.add ServerAddresses * %s\n", svc.Resolver)
	fmt.Fprintf(&b, "d.add SupplementalMatchDomains * %s\n", strings.Join(svc.Zones, " "))
	fmt.Fprintf(&b, "set %s\n", svc.dnsKey())
	return b.String()
}

// storeRemoveScript removes the given keys, each service's DNS entry before its IPv4 entry. A key
// that is not exactly ours is dropped, so this can never name another program's key. It returns ""
// when nothing is left to remove.
func storeRemoveScript(keys []string) string {
	own := make([]string, 0, len(keys))
	for _, key := range keys {
		if storeOwnKey.MatchString(key) {
			own = append(own, key)
		}
	}
	service := func(key string) string { return strings.TrimSuffix(strings.TrimSuffix(key, "/DNS"), "/IPv4") }
	sort.Slice(own, func(i, j int) bool {
		if si, sj := service(own[i]), service(own[j]); si != sj {
			return si < sj
		}
		return strings.HasSuffix(own[i], "/DNS") && !strings.HasSuffix(own[j], "/DNS")
	})
	var b strings.Builder
	for _, key := range own {
		fmt.Fprintf(&b, "remove %s\n", key)
	}
	return b.String()
}

func storeListScript() string { return "list " + dnsStoreListPattern + "\n" }

func storeShowScript(svc storeService) string { return "show " + svc.dnsKey() + "\n" }

// parseStoreKeys reads the output of `list` and returns the keys that are exactly ours.
func parseStoreKeys(out string) []string {
	var keys []string
	for _, line := range strings.Split(out, "\n") {
		_, key, ok := strings.Cut(line, "] = ")
		key = strings.TrimSpace(key)
		if ok && storeOwnKey.MatchString(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// storeDNS is what a DNS entry in the store holds.
type storeDNS struct {
	Servers []netip.Addr
	Zones   []string
}

// parseStoreDNS reads the output of `show State:/Network/Service/<id>/DNS`. It reports false when the
// output is not a dictionary (no such key, or an error message).
func parseStoreDNS(out string) (storeDNS, bool) {
	if !strings.Contains(out, "<dictionary>") {
		return storeDNS{}, false
	}
	var entry storeDNS
	var in string
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "ServerAddresses : <array>"):
			in = "servers"
		case strings.HasPrefix(line, "SupplementalMatchDomains : <array>"):
			in = "zones"
		case line == "}" || strings.HasSuffix(line, "<array> {"):
			in = ""
		case in != "":
			_, value, ok := strings.Cut(line, " : ")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			if in == "servers" {
				if addr, err := netip.ParseAddr(value); err == nil {
					entry.Servers = append(entry.Servers, addr)
				}
			} else {
				entry.Zones = append(entry.Zones, strings.ToLower(value))
			}
		}
	}
	return entry, true
}

// matches reports whether the store entry holds exactly this service: one server, the same zones,
// in any order.
func (s storeService) matches(e storeDNS) bool {
	if len(e.Servers) != 1 || e.Servers[0] != s.Resolver || len(e.Zones) != len(s.Zones) {
		return false
	}
	have := append([]string(nil), e.Zones...)
	sort.Strings(have)
	for i := range have {
		if have[i] != s.Zones[i] {
			return false
		}
	}
	return true
}
