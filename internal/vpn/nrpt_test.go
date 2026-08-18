package vpn

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestParseNrptRules(t *testing.T) {
	out := "\r\n.a.example,a.example|172.24.1.1\r\n.b.example|172.24.2.1,172.24.2.2\r\ngarbage line\r\n.c.example|not-an-ip\r\n"
	got := parseNrptRules(out)
	want := map[string]netip.Addr{
		"a.example": netip.MustParseAddr("172.24.1.1"),
		"b.example": netip.MustParseAddr("172.24.2.1"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(parseNrptRules("")) != 0 {
		t.Fatal("empty output must give no rules")
	}
}

func TestParseEffectiveNrptPolicyKeepsManagedConflicts(t *testing.T) {
	out := ".alpha.runos.xyz,alpha.runos.xyz|10.1.0.1\r\n.alpha.runos.xyz|192.0.2.53\r\n.beta.runos.xyz,beta.runos.xyz|10.2.0.1\r\n"
	got := parseEffectiveNrptPolicy(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d effective zones, want 2", len(got))
	}
	if len(got["alpha.runos.xyz"]) != 3 {
		t.Fatalf("alpha effective resolvers are %v", got["alpha.runos.xyz"])
	}
	if err := verifyEffectiveResolvers(got, []ResolverPlan{
		{Zone: "alpha.runos.xyz", Resolver: netip.MustParseAddr("10.1.0.1")},
		{Zone: "beta.runos.xyz", Resolver: netip.MustParseAddr("10.2.0.1")},
	}); err == nil {
		t.Fatal("the managed NRPT conflict was not reported")
	}
}
