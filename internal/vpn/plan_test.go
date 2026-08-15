package vpn

import (
	"encoding/json"
	"net/netip"
	"testing"
)

// The planner is the one place the desired-state document becomes an interface. These assert the
// requirement (goal 27, V5 "reconcile, do not apply"): a peer only for a cluster that is connected
// AND reachable, routes exactly the union of allowed IPs, split DNS only for the cluster's own
// zones, and loginRequired empties the peer set. Values are illustrative; nothing here names a
// real host or account.
const sampleDocument = `{
  "device": {"id": "d1", "address": "10.99.7.2/32", "session": {"expiresAt": "2030-01-01T00:00:00Z", "loginRequired": false}},
  "clusters": [
    {"cid": "bbb", "name": "second", "connected": true,
     "server": {"publicKey": "1L2kylCCPYBZrv9E0lE6ZT71PQJrs2f6L5Y6SmMIOEo=", "endpoint": "198.51.100.7:32768", "reachable": true, "reason": ""},
     "allowedIps": ["10.20.30.0/24"], "dns": {"resolver": "10.20.30.1", "zones": ["bbb.acct.example.net", "shop.example.com"]}, "peeredWith": []},
    {"cid": "aaa", "name": "first", "connected": true,
     "server": {"publicKey": "xC3MIw870r4X1m5HleBl3D0O9ZRo7kGnyviEBGKnqQM=", "endpoint": "203.0.113.5:32768", "reachable": true, "reason": ""},
     "allowedIps": ["10.10.10.0/24"], "dns": {"resolver": "10.10.10.1", "zones": ["aaa.acct.example.net"]}, "peeredWith": ["bbb"]},
    {"cid": "ccc", "name": "no-server", "connected": true,
     "server": {"publicKey": "", "endpoint": "", "reachable": false, "reason": "no VPN server installed"},
     "allowedIps": [], "dns": {"resolver": "", "zones": ["ccc.acct.example.net"]}, "peeredWith": []},
    {"cid": "ddd", "name": "not-connected", "connected": false,
     "server": {"publicKey": "1L2kylCCPYBZrv9E0lE6ZT71PQJrs2f6L5Y6SmMIOEo=", "endpoint": "198.51.100.9:32768", "reachable": true, "reason": ""},
     "allowedIps": ["10.40.40.0/24"], "dns": {"resolver": "10.40.40.1", "zones": ["ddd.acct.example.net"]}, "peeredWith": []}
  ],
  "revision": "abc"
}`

func mustDoc(t *testing.T, raw string) *Document {
	t.Helper()
	var doc Document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("sample document does not parse: %v", err)
	}
	return &doc
}

func TestBuildPlanHoldsAPeerOnlyForConnectedReachableClusters(t *testing.T) {
	plan, err := BuildPlan(mustDoc(t, sampleDocument))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Address.String() != "10.99.7.2/32" {
		t.Errorf("address = %s", plan.Address)
	}
	if len(plan.Peers) != 2 || plan.Peers[0].CID != "aaa" || plan.Peers[1].CID != "bbb" {
		t.Fatalf("peers = %+v, want aaa then bbb (sorted, no ccc, no ddd)", plan.Peers)
	}
	aaa := plan.Peers[0]
	if aaa.Endpoint != "203.0.113.5:32768" || aaa.Keepalive != KeepaliveSeconds {
		t.Errorf("aaa peer = %+v", aaa)
	}
	// base64 xC3M... decodes to 32 bytes; the UAPI wants hex.
	if len(aaa.PublicKeyHex) != 64 {
		t.Errorf("public key hex = %q", aaa.PublicKeyHex)
	}
	if len(aaa.AllowedIPs) != 1 || aaa.AllowedIPs[0].String() != "10.10.10.0/24" {
		t.Errorf("aaa allowed ips = %v", aaa.AllowedIPs)
	}
}

func TestBuildPlanRoutesTheUnionOfAllowedIPsAndSteersOnlyTheClustersZones(t *testing.T) {
	plan, err := BuildPlan(mustDoc(t, sampleDocument))
	if err != nil {
		t.Fatal(err)
	}
	if got := prefixStrings(plan.Routes); len(got) != 2 || got[0] != "10.10.10.0/24" || got[1] != "10.20.30.0/24" {
		t.Errorf("routes = %v", got)
	}
	// ccc's zone is not steered (no resolver, not reachable); ddd's is not (not connected).
	want := map[string]string{"aaa.acct.example.net": "10.10.10.1", "bbb.acct.example.net": "10.20.30.1", "shop.example.com": "10.20.30.1"}
	if len(plan.Resolvers) != len(want) {
		t.Fatalf("resolvers = %+v", plan.Resolvers)
	}
	for _, r := range plan.Resolvers {
		if want[r.Zone] != r.Resolver.String() {
			t.Errorf("zone %s -> %s, want %s", r.Zone, r.Resolver, want[r.Zone])
		}
	}
}

func TestBuildPlanEmptiesThePeersWhenLoginIsRequired(t *testing.T) {
	doc := mustDoc(t, sampleDocument)
	doc.Device.Session.LoginRequired = true
	plan, err := BuildPlan(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.LoginRequired || len(plan.Peers) != 0 || len(plan.Routes) != 0 || len(plan.Resolvers) != 0 {
		t.Errorf("plan = %+v, want no peers, routes or resolvers", plan)
	}
}

func TestBuildPlanRefusesAMalformedDocumentRatherThanSkippingAPart(t *testing.T) {
	doc := mustDoc(t, sampleDocument)
	doc.Clusters[0].Server.PublicKey = "not-a-key"
	if _, err := BuildPlan(doc); err == nil {
		t.Error("a bad server key must fail the plan")
	}
	doc = mustDoc(t, sampleDocument)
	doc.Clusters[1].AllowedIPs = []string{"10.10.10.0"}
	if _, err := BuildPlan(doc); err == nil {
		t.Error("an allowed ip that is not a prefix must fail the plan")
	}
}

func TestDiffPrefixesAddsWhatIsMissingAndRemovesWhatIsUnwanted(t *testing.T) {
	have := []netip.Prefix{netip.MustParsePrefix("10.10.10.0/24"), netip.MustParsePrefix("10.50.0.0/24")}
	want := []netip.Prefix{netip.MustParsePrefix("10.10.10.0/24"), netip.MustParsePrefix("10.20.30.0/24")}
	diff := DiffPrefixes(have, want)
	if got := prefixStrings(diff.Add); len(got) != 1 || got[0] != "10.20.30.0/24" {
		t.Errorf("add = %v", got)
	}
	if got := prefixStrings(diff.Remove); len(got) != 1 || got[0] != "10.50.0.0/24" {
		t.Errorf("remove = %v", got)
	}
	if d := DiffPrefixes(want, want); len(d.Add)+len(d.Remove) != 0 {
		t.Errorf("identical sets must diff to nothing, got %+v", d)
	}
}

func TestDiffResolversRewritesAChangedResolverAndDropsAStaleZone(t *testing.T) {
	have := map[string]netip.Addr{
		"aaa.acct.example.net": netip.MustParseAddr("10.10.10.1"),
		"old.acct.example.net": netip.MustParseAddr("10.9.9.1"),
		"bbb.acct.example.net": netip.MustParseAddr("10.0.0.99"),
	}
	want := []ResolverPlan{
		{Zone: "aaa.acct.example.net", Resolver: netip.MustParseAddr("10.10.10.1")},
		{Zone: "bbb.acct.example.net", Resolver: netip.MustParseAddr("10.20.30.1")},
	}
	diff := DiffResolvers(have, want)
	if len(diff.Set) != 1 || diff.Set[0].Zone != "bbb.acct.example.net" {
		t.Errorf("set = %+v (aaa unchanged must not be rewritten)", diff.Set)
	}
	if len(diff.Remove) != 1 || diff.Remove[0] != "old.acct.example.net" {
		t.Errorf("remove = %v", diff.Remove)
	}
}

func prefixStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p.String())
	}
	return out
}
