package vpn

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseScutilDNSKeepsMultipleClusterResolvers(t *testing.T) {
	output := `DNS configuration (for scoped queries)

resolver #1
  domain   : alpha.runos.xyz
  nameserver[0] : 10.1.0.1

resolver #2
  domain   : beta.runos.xyz
  nameserver[0] : 10.2.0.1
`
	got := parseScutilDNS(output)
	if len(got) != 2 {
		t.Fatalf("parsed %d scoped zones, want 2", len(got))
	}
	if got["alpha.runos.xyz"][0].String() != "10.1.0.1" {
		t.Fatalf("alpha resolver is %v", got["alpha.runos.xyz"])
	}
	if got["beta.runos.xyz"][0].String() != "10.2.0.1" {
		t.Fatalf("beta resolver is %v", got["beta.runos.xyz"])
	}
}

func TestVerifyEffectiveResolversReportsManagedConflict(t *testing.T) {
	effective := map[string][]netip.Addr{
		"alpha.runos.xyz": {
			netip.MustParseAddr("10.1.0.1"),
			netip.MustParseAddr("192.0.2.53"),
		},
	}
	expected := []ResolverPlan{{Zone: "alpha.runos.xyz", Resolver: netip.MustParseAddr("10.1.0.1")}}
	err := verifyEffectiveResolvers(effective, expected)
	if err == nil || !strings.Contains(err.Error(), "overridden") {
		t.Fatalf("managed resolver conflict was not reported: %v", err)
	}
}
