package vpn

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

/*
`runos vpn diagnose` says what it sees about private DNS (FCR 1730, H5).

It reads only what a person without root can read: the resolver files, which are world-readable, and
the text of `scutil --dns`. The finding is a pure function of those two, so it is tested on parsed inputs.
*/

var (
	probeZone     = "zone.example.test"
	probeResolver = netip.MustParseAddr("192.0.2.53")
	probeOther    = netip.MustParseAddr("198.51.100.9")
)

func goodFile(age time.Duration) ResolverFileFact {
	return ResolverFileFact{Present: true, Managed: true, Resolver: probeResolver, Age: age}
}

func TestDNSFindings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		probe DNSProbe
		want  []string // fragments, one per expected finding; nil means no finding
	}{
		{
			name: "file on disk, macOS does not list it",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: goodFile(95 * time.Second)},
				Effective: map[string][]netip.Addr{},
			},
			want: []string{"resolver file for " + probeZone + " is on disk but macOS does not list it", "95 seconds after the write", "turn the VPN off and on"},
		},
		{
			name: "everything is in order",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: goodFile(time.Minute)},
				Effective: map[string][]netip.Addr{probeZone: {probeResolver}},
			},
		},
		{
			name:  "no zone is expected",
			probe: DNSProbe{Effective: map[string][]netip.Addr{}},
		},
		{
			name: "the file is missing",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{},
				Effective: map[string][]netip.Addr{},
			},
			want: []string{"there is no resolver file for " + probeZone, "turn the VPN off and on"},
		},
		{
			name: "the file is empty",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: {Present: true, Empty: true, Age: 5 * time.Second}},
				Effective: map[string][]netip.Addr{},
			},
			want: []string{"resolver file for " + probeZone + " is empty", "turn the VPN off and on"},
		},
		{
			name: "a hand-authored file is in the way",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: {Present: true, Managed: false, Resolver: probeOther}},
				Effective: map[string][]netip.Addr{probeZone: {probeOther}},
			},
			want: []string{"resolver file for " + probeZone + " that RunOS did not write", "remove it"},
		},
		{
			name: "our file names a different resolver than the cluster expects",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: {Present: true, Managed: true, Resolver: probeOther, Age: time.Minute}},
				Effective: map[string][]netip.Addr{probeZone: {probeOther}},
			},
			want: []string{"holds " + probeOther.String() + " but the cluster resolver is " + probeResolver.String()},
		},
		{
			name: "another resolver answers for the zone",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: goodFile(time.Minute)},
				Effective: map[string][]netip.Addr{probeZone: {probeOther}},
			},
			want: []string{"macOS sends " + probeZone + " to " + probeOther.String(), "another VPN or DNS tool"},
		},
		{
			name: "the Mac's DNS view cannot be read",
			probe: DNSProbe{
				Zones:        []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:        map[string]ResolverFileFact{probeZone: goodFile(time.Minute)},
				EffectiveErr: "scutil: exit status 1",
			},
			want: []string{"could not read this Mac's DNS settings", "scutil: exit status 1"},
		},
		{
			name: "age unknown is left out of the sentence",
			probe: DNSProbe{
				Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
				Files:     map[string]ResolverFileFact{probeZone: goodFile(-1)},
				Effective: map[string][]netip.Addr{},
			},
			want: []string{"is on disk but macOS does not list it"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dnsFindings(tc.probe)
			if len(got) != len(tc.want) && !(len(tc.want) > 1 && len(got) == 1) {
				t.Fatalf("findings = %q, want %d line(s) matching %q", got, len(tc.want), tc.want)
			}
			joined := strings.Join(got, "\n")
			for _, fragment := range tc.want {
				if !strings.Contains(joined, fragment) {
					t.Errorf("findings %q lack %q", joined, fragment)
				}
			}
		})
	}
}

func TestDNSFindingAgeWording(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{0, "0 seconds after the write"}, {1 * time.Second, "1 second after the write"},
		{95 * time.Second, "95 seconds after the write"}, {5 * time.Minute, "5 minutes after the write"},
	} {
		got := dnsFindings(DNSProbe{
			Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}},
			Files:     map[string]ResolverFileFact{probeZone: goodFile(tc.age)},
			Effective: map[string][]netip.Addr{},
		})
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("age %v: %q, want %q", tc.age, got, tc.want)
		}
	}
}

// The collector reads the world-readable resolver directory and the scutil text, and never needs root.
func TestProbeMacDNSReadsFilesAndTheListing(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	write := func(name, body string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("ours.example.test", resolverMarker+"\nnameserver 192.0.2.53\n", 90*time.Second)
	write("empty.example.test", "", time.Second)
	write("theirs.example.test", "nameserver 198.51.100.1\n", time.Hour)
	zones := []ResolverPlan{
		{Zone: "ours.example.test", Resolver: probeResolver}, {Zone: "empty.example.test", Resolver: probeResolver},
		{Zone: "theirs.example.test", Resolver: probeResolver}, {Zone: "gone.example.test", Resolver: probeResolver},
	}
	scutil := func() (string, error) {
		return "resolver #2\n  domain   : ours.example.test\n  nameserver[0] : 192.0.2.53\n", nil
	}

	probe := ProbeMacDNS(dir, zones, scutil, now)

	ours := probe.Files["ours.example.test"]
	if !ours.Present || !ours.Managed || ours.Resolver != probeResolver || ours.Age != 90*time.Second {
		t.Errorf("ours = %+v", ours)
	}
	if f := probe.Files["empty.example.test"]; !f.Present || !f.Empty {
		t.Errorf("empty = %+v", f)
	}
	if f := probe.Files["theirs.example.test"]; !f.Present || f.Managed {
		t.Errorf("theirs = %+v", f)
	}
	if f := probe.Files["gone.example.test"]; f.Present {
		t.Errorf("gone = %+v", f)
	}
	if len(probe.Effective["ours.example.test"]) != 1 || probe.EffectiveErr != "" {
		t.Errorf("effective = %v err = %q", probe.Effective, probe.EffectiveErr)
	}

	failing := ProbeMacDNS(dir, zones, func() (string, error) { return "", errors.New("scutil: not found") }, now)
	if failing.EffectiveErr == "" {
		t.Error("a scutil failure must be recorded, not dropped")
	}
}

// What the report plugs in: zones come from the connected clusters in the daemon status.
func TestZonesFromStatusCoversConnectedClustersOnly(t *testing.T) {
	st := &Status{Running: true, Clusters: []ClusterStatus{
		{CID: "aaa", Connected: true, Resolver: "192.0.2.53", Zones: []string{"a.example.test", "b.example.test"}},
		{CID: "bbb", Connected: false, Resolver: "192.0.2.54", Zones: []string{"c.example.test"}},
		{CID: "ccc", Connected: true, Resolver: "", Zones: []string{"d.example.test"}},
		{CID: "ddd", Connected: true, Resolver: "not an address", Zones: []string{"e.example.test"}},
	}}
	got := ZonesFromStatus(st)
	if len(got) != 2 || got[0].Zone != "a.example.test" || got[1].Zone != "b.example.test" || got[0].Resolver != probeResolver {
		t.Errorf("zones = %+v, want the two zones of the connected cluster with a resolver", got)
	}
	if ZonesFromStatus(nil) != nil {
		t.Error("nil status has no zones")
	}
}

// The report carries both the daemon's pending state and what diagnose saw on disk.
func TestReportSaysWhatItSeesAboutPrivateDNS(t *testing.T) {
	st := &Status{Running: true, DNS: DNSStatus{Mode: "native", Pending: true, Error: dnsPendingText},
		Clusters: []ClusterStatus{{CID: "aaa", Connected: true, Reachable: true, PeerUp: true, Resolver: probeResolver.String(), Zones: []string{probeZone}}}}
	probe := DNSProbe{
		Zones:     ZonesFromStatus(st),
		Files:     map[string]ResolverFileFact{probeZone: goodFile(95 * time.Second)},
		Effective: map[string][]netip.Addr{},
	}

	r := BuildReport(ReportInput{Now: time.Now(), OS: "darwin", Socket: SocketProbe{State: "reachable"}, SignedIn: true, Status: st, DNS: &probe})

	joined := strings.Join(r.Findings, "\n")
	for _, want := range []string{"still being applied", "is on disk but macOS does not list it", "95 seconds after the write"} {
		if !strings.Contains(joined, want) {
			t.Errorf("findings lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "private DNS is not working") {
		t.Errorf("a pending zone is not a failure:\n%s", joined)
	}
}

func TestReportWithoutAProbeOrWithADownTunnelAddsNoDNSFinding(t *testing.T) {
	probe := DNSProbe{Zones: []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}}, Files: map[string]ResolverFileFact{}, Effective: map[string][]netip.Addr{}}
	down := BuildReport(ReportInput{Now: time.Now(), Socket: SocketProbe{State: "reachable"}, SignedIn: true, Status: &Status{Running: false}, DNS: &probe})
	none := BuildReport(ReportInput{Now: time.Now(), Socket: SocketProbe{State: "reachable"}, SignedIn: true, Status: &Status{Running: true, Clusters: []ClusterStatus{{Connected: true, Reachable: true, PeerUp: true}}}})
	for name, r := range map[string]Report{"tunnel down": down, "no probe": none} {
		for _, f := range r.Findings {
			if strings.Contains(f, "resolver file") {
				t.Errorf("%s: unexpected DNS finding %q", name, f)
			}
		}
	}
}
