package vpn

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

/*
The dynamic-store half of macOS split DNS (FCR 1730).

Everything here is pure text: the script fed to scutil, the keys it names, and the parsers for what
scutil prints back. No test starts scutil or touches the store; fakeScutilStore (dns_store_fake_test.go)
plays it.
*/

// storePlans builds resolver plans from zone, resolver pairs. The darwin tests have plans() for the
// same job, but this file builds on every platform.
func storePlans(pairs ...string) []ResolverPlan {
	var out []ResolverPlan
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ResolverPlan{Zone: pairs[i], Resolver: netip.MustParseAddr(pairs[i+1])})
	}
	return out
}

func TestStoreApplyScriptText(t *testing.T) {
	client := netip.MustParseAddr("192.0.2.2")
	for _, tc := range []struct {
		name   string
		plan   []ResolverPlan
		iface  string
		client netip.Addr
		want   string
	}{
		{
			name: "one zone", plan: storePlans("a.example.test", "192.0.2.53"), iface: "utun9", client: client,
			want: "d.init\n" +
				"d.add Addresses * 192.0.2.2\n" +
				"d.add SubnetMasks * 255.255.255.255\n" +
				"d.add InterfaceName utun9\n" +
				"set State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4\n" +
				"d.init\n" +
				"d.add ServerAddresses * 192.0.2.53\n" +
				"d.add SupplementalMatchDomains * a.example.test\n" +
				"set State:/Network/Service/com.runos.vpn.192-0-2-53/DNS\n",
		},
		{
			name: "several zones of one resolver share one entry, sorted",
			plan: storePlans("b.example.test", "192.0.2.53", "a.example.test", "192.0.2.53"), iface: "utun9", client: client,
			want: "d.init\n" +
				"d.add Addresses * 192.0.2.2\n" +
				"d.add SubnetMasks * 255.255.255.255\n" +
				"d.add InterfaceName utun9\n" +
				"set State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4\n" +
				"d.init\n" +
				"d.add ServerAddresses * 192.0.2.53\n" +
				"d.add SupplementalMatchDomains * a.example.test b.example.test\n" +
				"set State:/Network/Service/com.runos.vpn.192-0-2-53/DNS\n",
		},
		{
			name: "no interface known: the DNS entry only",
			plan: storePlans("a.example.test", "192.0.2.53"),
			want: "d.init\n" +
				"d.add ServerAddresses * 192.0.2.53\n" +
				"d.add SupplementalMatchDomains * a.example.test\n" +
				"set State:/Network/Service/com.runos.vpn.192-0-2-53/DNS\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services, err := dnsStoreServices(tc.plan)
			if err != nil || len(services) != 1 {
				t.Fatalf("services = %v, %v", services, err)
			}
			if got := storeApplyScript(services[0], tc.iface, tc.client); got != tc.want {
				t.Errorf("script:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestStoreServicesOnePerResolver(t *testing.T) {
	got, err := dnsStoreServices(storePlans(
		"b.example.test", "192.0.2.54", "A.Example.Test.", "192.0.2.53", "c.example.test", "192.0.2.54"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var zones [][]string
	for _, s := range got {
		ids = append(ids, s.ID)
		zones = append(zones, s.Zones)
	}
	if want := []string{"com.runos.vpn.192-0-2-53", "com.runos.vpn.192-0-2-54"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if want := [][]string{{"a.example.test"}, {"b.example.test", "c.example.test"}}; !reflect.DeepEqual(zones, want) {
		t.Errorf("zones = %v, want %v (lower-cased, no trailing dot, sorted)", zones, want)
	}
	if none, err := dnsStoreServices(nil); err != nil || len(none) != 0 {
		t.Errorf("no zones = %v, %v; want nothing", none, err)
	}
}

// A zone name goes into a script that scutil executes line by line, so a name that carries a line
// break or a space could add commands. It is refused before any script exists.
func TestStoreServicesRefuseZonesThatCouldInjectCommands(t *testing.T) {
	for _, zone := range []string{
		"a.example.test\nremove State:/Network/Service/other/DNS",
		"a.example.test b.example.test",
		"a.example.test;x",
		"",
		".",
		"-bad.example.test",
		"a..example.test",
		`quo"te.example.test`,
	} {
		t.Run(strings.ReplaceAll(zone, "\n", "\\n"), func(t *testing.T) {
			if _, err := dnsStoreServices([]ResolverPlan{{Zone: zone, Resolver: netip.MustParseAddr("192.0.2.53")}}); err == nil {
				t.Errorf("zone %q was accepted", zone)
			}
		})
	}
}

func TestStoreRemoveScriptText(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		want string
	}{
		{"nothing to remove", nil, ""},
		{
			"each service loses its DNS entry before its IPv4 entry",
			[]string{
				"State:/Network/Service/com.runos.vpn.192-0-2-54/IPv4",
				"State:/Network/Service/com.runos.vpn.192-0-2-53/DNS",
				"State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4",
			},
			"remove State:/Network/Service/com.runos.vpn.192-0-2-53/DNS\n" +
				"remove State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4\n" +
				"remove State:/Network/Service/com.runos.vpn.192-0-2-54/IPv4\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storeRemoveScript(tc.keys); got != tc.want {
				t.Errorf("script:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestStoreRemoveScriptNeverNamesAKeyThatIsNotOurs(t *testing.T) {
	got := storeRemoveScript([]string{
		"State:/Network/Service/ABCDEF/DNS",
		"State:/Network/Service/com.runos.vpnx/DNS",
		"State:/Network/Service/com.runos.vpn/../other/DNS",
		"Setup:/Network/Service/com.runos.vpn/DNS",
		"State:/Network/Service/com.runos.vpn/DNS",
	})
	if want := "remove State:/Network/Service/com.runos.vpn/DNS\n"; got != want {
		t.Errorf("script = %q, want only our own key: %q", got, want)
	}
}

const storeListFixture = `  subKey [0] = State:/Network/Service/25B1EF23-4C7A-446A-B3FF-FCA0E4B011A3/DNS
  subKey [1] = State:/Network/Service/com.example.other/DNS
  subKey [2] = State:/Network/Service/com.runos.vpn.192-0-2-53/DNS
  subKey [3] = State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4
  subKey [4] = State:/Network/Service/com.runos.vpnx/DNS
  subKey [5] = State:/Network/Service/com.runos.vpn/DNS
  subKey [6] = State:/Network/Service/com.runos.vpn.192-0-2-53/Proxies
  subKey [7] = State:/Network/Service/com.runos.vpn.../DNS
`

func TestParseStoreKeysKeepsOnlyOurs(t *testing.T) {
	want := []string{
		"State:/Network/Service/com.runos.vpn.192-0-2-53/DNS",
		"State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4",
		"State:/Network/Service/com.runos.vpn/DNS",
	}
	if got := parseStoreKeys(storeListFixture); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if got := parseStoreKeys("  no keys\n"); len(got) != 0 {
		t.Errorf("keys from an empty listing = %v", got)
	}
}

func TestParseStoreDNS(t *testing.T) {
	const present = `<dictionary> {
  ServerAddresses : <array> {
    0 : 192.0.2.53
    1 : 192.0.2.54
  }
  SupplementalMatchDomains : <array> {
    0 : a.example.test
    1 : b.example.test
  }
}
`
	got, ok := parseStoreDNS(present)
	if !ok {
		t.Fatal("entry not found")
	}
	wantServers := []netip.Addr{netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("192.0.2.54")}
	if !reflect.DeepEqual(got.Servers, wantServers) || !reflect.DeepEqual(got.Zones, []string{"a.example.test", "b.example.test"}) {
		t.Errorf("entry = %+v", got)
	}
	for _, absent := range []string{"  No such key\n", "", "SCDynamicStoreCopyValue() failed: Permission denied\n"} {
		if _, ok := parseStoreDNS(absent); ok {
			t.Errorf("%q was read as an entry", absent)
		}
	}
}

func TestStoreServiceMatchesWhatTheStoreHolds(t *testing.T) {
	svc := storeService{ID: "com.runos.vpn.192-0-2-53", Resolver: netip.MustParseAddr("192.0.2.53"), Zones: []string{"a.example.test", "b.example.test"}}
	same := storeDNS{Servers: []netip.Addr{svc.Resolver}, Zones: []string{"b.example.test", "a.example.test"}}
	if !svc.matches(same) {
		t.Error("the same set in another order must match")
	}
	for name, e := range map[string]storeDNS{
		"another resolver": {Servers: []netip.Addr{netip.MustParseAddr("192.0.2.99")}, Zones: svc.Zones},
		"an extra server":  {Servers: []netip.Addr{svc.Resolver, netip.MustParseAddr("192.0.2.99")}, Zones: svc.Zones},
		"a missing zone":   {Servers: []netip.Addr{svc.Resolver}, Zones: []string{"a.example.test"}},
		"an extra zone":    {Servers: []netip.Addr{svc.Resolver}, Zones: []string{"a.example.test", "b.example.test", "c.example.test"}},
	} {
		if svc.matches(e) {
			t.Errorf("%s matched", name)
		}
	}
}

// The scripts are idempotent by construction: every apply starts from d.init and sets whole keys, so
// a second run leaves the store exactly as the first did and adds nothing.
func TestStoreApplyTwiceLeavesOneSetOfKeys(t *testing.T) {
	store := newFakeScutilStore()
	services, err := dnsStoreServices(storePlans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.54"))
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for _, svc := range services {
			if _, err := store.run(storeApplyScript(svc, "utun9", netip.MustParseAddr("192.0.2.2"))); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := []string{
		"State:/Network/Service/com.runos.vpn.192-0-2-53/DNS",
		"State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4",
		"State:/Network/Service/com.runos.vpn.192-0-2-54/DNS",
		"State:/Network/Service/com.runos.vpn.192-0-2-54/IPv4",
	}
	if got := store.sortedKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	out, _ := store.run("show " + services[0].dnsKey() + "\n")
	entry, ok := parseStoreDNS(out)
	if !ok || !services[0].matches(entry) {
		t.Errorf("the entry read back = %+v, %v", entry, ok)
	}
}

// IPv4 first, DNS second, in one script: the DNS entry belongs to a service configd can already see.
func TestStoreApplyWritesTheIPv4EntryBeforeTheDNSEntry(t *testing.T) {
	store := newFakeScutilStore()
	services, _ := dnsStoreServices(storePlans("a.example.test", "192.0.2.53"))
	if _, err := store.run(storeApplyScript(services[0], "utun9", netip.MustParseAddr("192.0.2.2"))); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4",
		"State:/Network/Service/com.runos.vpn.192-0-2-53/DNS",
	}
	if got := store.setKeys(); !reflect.DeepEqual(got, want) {
		t.Errorf("set order = %v, want %v", got, want)
	}
}

func TestParseStoreIPv4(t *testing.T) {
	const present = `<dictionary> {
  Addresses : <array> {
    0 : 192.0.2.2
  }
  InterfaceName : utun9
  SubnetMasks : <array> {
    0 : 255.255.255.255
  }
}
`
	iface, addr, ok := parseStoreIPv4(present)
	if !ok || iface != "utun9" || addr != netip.MustParseAddr("192.0.2.2") {
		t.Errorf("iface=%q addr=%v ok=%v", iface, addr, ok)
	}
	if _, _, ok := parseStoreIPv4("  No such key\n"); ok {
		t.Error("a missing key was read as an entry")
	}
}
