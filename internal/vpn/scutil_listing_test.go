package vpn

import (
	"net/netip"
	"reflect"
	"testing"
)

// macOS26Listing is the shape `scutil --dns` has on macOS 26 with a VPN's supplemental resolvers: a
// default resolver, the built-in multicast domains, supplemental resolvers with their flags and
// order, a search domain, an IPv6 server, and the second "scoped queries" section that repeats the
// default resolver per interface. Addresses are documentation addresses.
const macOS26Listing = `DNS configuration

resolver #1
  search domain[0] : lan.example.test
  nameserver[0] : 198.51.100.1
  nameserver[1] : 2001:db8::1
  if_index : 18 (en7)
  flags    : Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  domain   : local
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300000

resolver #3
  domain   : 254.169.in-addr.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300200

resolver #4
  domain   : Cluster-A.Example.Test.
  nameserver[0] : 192.0.2.53
  flags    : Supplemental, Request A records
  reach    : 0x00000002 (Reachable)
  order    : 100000

resolver #5
  domain   : apps.example.test
  nameserver[0] : 192.0.2.53
  flags    : Supplemental, Request A records
  reach    : 0x00000002 (Reachable)
  order    : 100200

resolver #6
  domain   : corp.example.test
  nameserver[0] : 203.0.113.7
  nameserver[1] : 203.0.113.8
  if_index : 21 (utun4)
  flags    : Supplemental, Request A records
  reach    : 0x00000002 (Reachable)
  order    : 100400

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 198.51.100.1
  if_index : 18 (en7)
  flags    : Scoped, Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  nameserver[0] : 198.51.100.1
  if_index : 14 (en0)
  flags    : Scoped, Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)
`

func TestParseScutilDNSReadsTheMacOS26Listing(t *testing.T) {
	got := parseScutilDNS(macOS26Listing)
	want := map[string][]netip.Addr{
		"local":                  nil,
		"254.169.in-addr.arpa":   nil,
		"cluster-a.example.test": {netip.MustParseAddr("192.0.2.53")},
		"apps.example.test":      {netip.MustParseAddr("192.0.2.53")},
		"corp.example.test":      {netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("203.0.113.8")},
	}
	for zone, servers := range want {
		if servers == nil {
			if _, listed := got[zone]; listed {
				t.Errorf("%s has no nameserver and must not be listed as served: %v", zone, got[zone])
			}
			continue
		}
		if !reflect.DeepEqual(got[zone], servers) {
			t.Errorf("%s = %v, want %v", zone, got[zone], servers)
		}
	}
	if _, listed := got["lan.example.test"]; listed {
		t.Error("a search domain is not a match domain")
	}
}

func TestVerificationOverTheMacOS26Listing(t *testing.T) {
	listed := parseScutilDNS(macOS26Listing)
	for _, tc := range []struct {
		name        string
		plan        []ResolverPlan
		wantOK      bool
		wantPending []string
		wantOver    bool
	}{
		{"our two supplemental zones are listed with our resolver", storePlans("cluster-a.example.test", "192.0.2.53", "apps.example.test", "192.0.2.53"), true, nil, false},
		{"one zone is listed and one is not yet", storePlans("cluster-a.example.test", "192.0.2.53", "later.example.test", "192.0.2.53"), false, []string{"later.example.test"}, false},
		{"another VPN's zone served by another resolver is an override for us", storePlans("corp.example.test", "192.0.2.53"), false, nil, true},
		{"a built-in multicast domain is never ours", storePlans("local", "192.0.2.53"), false, []string{"local"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := checkEffectiveResolvers(listed, tc.plan)
			if check.OK() != tc.wantOK || !reflect.DeepEqual(check.Pending, tc.wantPending) || (check.Overridden != nil) != tc.wantOver {
				t.Errorf("check = %+v, want ok=%v pending=%v override=%v", check, tc.wantOK, tc.wantPending, tc.wantOver)
			}
		})
	}
}
