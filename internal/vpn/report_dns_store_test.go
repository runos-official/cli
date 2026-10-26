package vpn

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

/*
`runos vpn diagnose` and the dynamic store (FCR 1730). It reads the store with `scutil` show and list,
which need no root, and says which of the three things are in place for each zone: the resolver file,
the store entry, and the zone listed by macOS.
*/

func storeFact(zones ...string) StoreFact {
	f := StoreFact{Checked: true, Zones: map[string]netip.Addr{}}
	for _, z := range zones {
		f.Zones[z] = probeResolver
	}
	return f
}

func TestDNSFindingsKnowTheStoreEntry(t *testing.T) {
	zones := []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}}
	listedOurs := map[string][]netip.Addr{probeZone: {probeResolver}}
	for _, tc := range []struct {
		name  string
		probe DNSProbe
		want  []string // fragments that must all be in the one finding; nil means no finding
	}{
		{
			name:  "listed through the store alone: nothing is wrong even without a file",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}, Effective: listedOurs, Store: storeFact(probeZone)},
		},
		{
			name:  "everything in place and listed",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{probeZone: goodFile(time.Minute)}, Effective: listedOurs, Store: storeFact(probeZone)},
		},
		{
			name:  "store entry and file in place, macOS lists nothing",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{probeZone: goodFile(95 * time.Second)}, Effective: map[string][]netip.Addr{}, Store: storeFact(probeZone)},
			want:  []string{"dynamic-store entry and the resolver file", "macOS does not list it", "turn the VPN off and on"},
		},
		{
			name:  "store entry only, macOS lists nothing",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}, Effective: map[string][]netip.Addr{}, Store: storeFact(probeZone)},
			want:  []string{"dynamic-store entry", "no resolver file", "macOS does not list it", "turn the VPN off and on"},
		},
		{
			name:  "neither mechanism is in place",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}, Effective: map[string][]netip.Addr{}, Store: storeFact()},
			want:  []string{"has not set up private DNS", "no dynamic-store entry and no resolver file", "turn the VPN off and on"},
		},
		{
			name:  "file only: the file findings stay as they were",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{probeZone: goodFile(95 * time.Second)}, Effective: map[string][]netip.Addr{}, Store: storeFact()},
			want:  []string{"resolver file for " + probeZone + " is on disk but macOS does not list it"},
		},
		{
			name: "another resolver answers for the zone",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}, Store: storeFact(probeZone),
				Effective: map[string][]netip.Addr{probeZone: {probeOther}}},
			want: []string{"sends " + probeZone + " to " + probeOther.String()},
		},
		{
			name:  "the store could not be read: the file findings are used",
			probe: DNSProbe{Zones: zones, Files: map[string]ResolverFileFact{}, Effective: map[string][]netip.Addr{}, Store: StoreFact{ReadErr: "scutil: not found"}},
			want:  []string{"there is no resolver file for " + probeZone},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dnsFindings(tc.probe)
			if (len(tc.want) == 0) != (len(got) == 0) || len(got) > 1 {
				t.Fatalf("findings = %q, want %v", got, tc.want)
			}
			for _, fragment := range tc.want {
				if !strings.Contains(strings.Join(got, "\n"), fragment) {
					t.Errorf("findings lack %q:\n%s", fragment, strings.Join(got, "\n"))
				}
			}
		})
	}
}

func TestDNSMechanismLinesSayWhatIsInPlace(t *testing.T) {
	probe := DNSProbe{
		Zones:     []ResolverPlan{{Zone: probeZone, Resolver: probeResolver}, {Zone: "b." + probeZone, Resolver: probeResolver}},
		Files:     map[string]ResolverFileFact{probeZone: goodFile(time.Minute)},
		Effective: map[string][]netip.Addr{probeZone: {probeResolver}},
		Store:     storeFact(probeZone),
	}
	got := dnsMechanismLines(probe)
	want := []string{
		"private DNS for " + probeZone + ": resolver file yes, dynamic-store entry yes, listed by macOS yes",
		"private DNS for b." + probeZone + ": resolver file no, dynamic-store entry no, listed by macOS no",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	unread := probe
	unread.Store = StoreFact{ReadErr: "boom"}
	if lines := dnsMechanismLines(unread); !strings.Contains(lines[0], "dynamic-store entry unknown") {
		t.Errorf("a store that could not be read must say unknown, got %q", lines[0])
	}
	unreadable := probe
	unreadable.EffectiveErr = "scutil failed"
	if lines := dnsMechanismLines(unreadable); !strings.Contains(lines[0], "listed by macOS unknown") {
		t.Errorf("a listing that could not be read must say unknown, got %q", lines[0])
	}
}

func TestProbeMacStoreReadsOnlyOurEntries(t *testing.T) {
	store := newFakeScutilStore()
	for _, svc := range mustServices(t, storePlans(probeZone, probeResolver.String(), "b."+probeZone, probeResolver.String())) {
		if _, err := store.run(storeApplyScript(svc, "utun9", netip.MustParseAddr("192.0.2.2"))); err != nil {
			t.Fatal(err)
		}
	}
	store.seed("State:/Network/Service/com.runos.vpnx/DNS", map[string][]string{
		"ServerAddresses":          {"<array> {", "  0 : 198.51.100.1", "}"},
		"SupplementalMatchDomains": {"<array> {", "  0 : other.example.test", "}"},
	})
	store.log = nil

	fact := ProbeMacStore(store.run)

	if !fact.Checked || fact.ReadErr != "" {
		t.Fatalf("fact = %+v", fact)
	}
	if len(fact.Zones) != 2 || fact.Zones[probeZone] != probeResolver || fact.Zones["b."+probeZone] != probeResolver {
		t.Errorf("zones = %v, want only the two of ours", fact.Zones)
	}
	if len(store.log) != 0 {
		t.Errorf("diagnose wrote to the store: %v", store.log)
	}
}

func TestProbeMacStoreReportsAnUnreadableStore(t *testing.T) {
	store := newFakeScutilStore()
	store.err = errors.New("scutil: not found")
	fact := ProbeMacStore(store.run)
	if fact.Checked || fact.ReadErr == "" {
		t.Errorf("fact = %+v, want unread with a reason", fact)
	}
}

func TestReportCarriesTheMechanismLines(t *testing.T) {
	st := &Status{Running: true, DNS: DNSStatus{Mode: "store", Available: true},
		Clusters: []ClusterStatus{{CID: "aaa", Connected: true, Reachable: true, PeerUp: true, Resolver: probeResolver.String(), Zones: []string{probeZone}}}}
	probe := DNSProbe{
		Zones:     ZonesFromStatus(st),
		Files:     map[string]ResolverFileFact{},
		Effective: map[string][]netip.Addr{probeZone: {probeResolver}},
		Store:     storeFact(probeZone),
	}

	r := BuildReport(ReportInput{Now: time.Now(), OS: "darwin", Socket: SocketProbe{State: "reachable"}, SignedIn: true, Status: st, DNS: &probe})

	if len(r.DNSMechanisms) != 1 || !strings.Contains(r.DNSMechanisms[0], "dynamic-store entry yes") {
		t.Fatalf("mechanisms = %q", r.DNSMechanisms)
	}
	if text := r.Text(); !strings.Contains(text, r.DNSMechanisms[0]) {
		t.Errorf("the text report lacks the mechanism line:\n%s", text)
	}
	for _, f := range r.Findings {
		if strings.Contains(f, "resolver file") {
			t.Errorf("a zone listed through the store must not raise a file finding: %q", f)
		}
	}
	down := BuildReport(ReportInput{Now: time.Now(), Socket: SocketProbe{State: "reachable"}, SignedIn: true, Status: &Status{Running: false}, DNS: &probe})
	if len(down.DNSMechanisms) != 0 {
		t.Errorf("a down tunnel has no mechanism lines: %q", down.DNSMechanisms)
	}
}

func mustServices(t *testing.T, plan []ResolverPlan) []storeService {
	t.Helper()
	services, err := dnsStoreServices(plan)
	if err != nil {
		t.Fatal(err)
	}
	return services
}
