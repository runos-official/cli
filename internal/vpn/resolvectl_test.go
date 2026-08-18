package vpn

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestResolvedDNSCommandsApplyOneCompleteMultiClusterState(t *testing.T) {
	commands := resolvedDNSCommands("runos0", netip.MustParseAddrPort("127.0.0.1:53001"), []ResolverPlan{
		{Zone: "alpha.runos.xyz", Resolver: netip.MustParseAddr("10.1.0.1")},
		{Zone: "beta.runos.xyz", Resolver: netip.MustParseAddr("10.2.0.1")},
	})
	want := [][]string{
		{"dns", "runos0", "127.0.0.1:53001"},
		{"domain", "runos0", "~alpha.runos.xyz", "~beta.runos.xyz"},
		{"default-route", "runos0", "no"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("resolved commands are %v, want %v", commands, want)
	}
}

func TestResolvedDNSCommandsUseLegacySyntaxForPort53Fallback(t *testing.T) {
	commands := resolvedDNSCommands("runos0", netip.MustParseAddrPort("172.24.8.10:53"), []ResolverPlan{
		{Zone: "alpha.runos.xyz", Resolver: netip.MustParseAddr("10.1.0.1")},
	})
	if got := commands[0][2]; got != "172.24.8.10" {
		t.Fatalf("port 53 fallback server is %q, want an address without extended-port syntax", got)
	}
}

func TestResolvedDNSProbeUsesTheVPNInterface(t *testing.T) {
	want := []string{"--interface=runos0", "query", "alpha.runos.xyz"}
	if got := resolvedDNSProbeCommand("runos0", "alpha.runos.xyz"); !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved probe command is %v, want %v", got, want)
	}
}
