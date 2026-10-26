//go:build probe && darwin

package vpn

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the probe's logic on a temp directory, a fake store and a fake macOS. They need no root,
// so `go test -tags probe -run TestProbeLogic ./internal/vpn` checks the probe itself.

// fakeMacForProbe lists a scratch zone a while after it first sees it, and the delay depends on the
// method that published it (the method number is inside the zone name). A negative delay never lists.
type fakeMacForProbe struct {
	t       time.Time
	dir     string
	store   *fakeScutilStore
	seen    map[string]time.Time
	delay   map[int]time.Duration
	flushes int
	nudges  int
	// rescuedByRepublish: a zone listed only after this long counts from the LAST publish, so a
	// re-publish at the nudge time matters. Zero means the first publish decides.
	nudgeErr error
}

func newFakeMacForProbe(t *testing.T) *fakeMacForProbe {
	dir := filepath.Join(t.TempDir(), "resolver")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeMacForProbe{
		t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), dir: dir, store: newFakeScutilStore(),
		seen: map[string]time.Time{}, delay: map[int]time.Duration{},
	}
}

func methodOf(zone string) int {
	var run, method, i int
	fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(zone, probeZonePrefix), probeZoneSuffix), "%d-%d-%d", &run, &method, &i)
	return method
}

func (m *fakeMacForProbe) visible(zone string) bool {
	if _, ok := m.seen[zone]; !ok {
		m.seen[zone] = m.t
	}
	d, ok := m.delay[methodOf(zone)]
	return ok && d >= 0 && m.t.Sub(m.seen[zone]) >= d
}

func (m *fakeMacForProbe) env(n int) *probeEnv {
	return &probeEnv{
		dir: m.dir,
		scutil: func() (string, error) {
			var b strings.Builder
			b.WriteString("DNS configuration\n\n")
			entries, _ := os.ReadDir(m.dir)
			for _, e := range entries {
				if m.visible(e.Name()) {
					fmt.Fprintf(&b, "resolver #2\n  domain   : %s\n  nameserver[0] : %s\n\n", e.Name(), probeNameserver)
				}
			}
			for _, key := range m.store.sortedKeys() {
				if !strings.HasSuffix(key, "/DNS") {
					continue
				}
				out, _ := m.store.run("show " + key + "\n")
				entry, _ := parseStoreDNS(out)
				for _, zone := range entry.Zones {
					if m.visible(zone) {
						fmt.Fprintf(&b, "resolver #3\n  domain   : %s\n  nameserver[0] : %s\n  flags    : Supplemental\n\n", zone, probeNameserver)
					}
				}
			}
			return b.String(), nil
		},
		store: m.store.run,
		flush: func() error { m.flushes++; return nil },
		nudge: func() error { m.nudges++; return m.nudgeErr },
		now:   func() time.Time { return m.t },
		sleep: func(d time.Duration) { m.t = m.t.Add(d) },
		out:   io.Discard, n: n, runID: "1",
		poll: 50 * time.Millisecond, giveUp: 30 * time.Second, nudgeAt: 8 * time.Second,
		stopped: func() bool { return false },
	}
}

func TestProbeLogicRecordsLatencyForFourMethodsInterleavedAndCleansUp(t *testing.T) {
	mac := newFakeMacForProbe(t)
	mac.delay = map[int]time.Duration{0: 2 * time.Second, 1: 1500 * time.Millisecond, 2: 900 * time.Millisecond, 3: 300 * time.Millisecond}
	var order []string
	env := mac.env(2)
	env.out = writerFunc(func(p []byte) { order = append(order, strings.Fields(string(p))[2]) })

	results, err := runProbe(env)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 8 {
		t.Fatalf("results = %d, want 2 runs of 4 methods", len(results))
	}
	if got := strings.Join(order, ""); got != "ABCDABCD" {
		t.Errorf("order = %s, want the methods interleaved ABCDABCD", got)
	}
	table := probeTable(results)
	for _, want := range []string{"A truncate+write file", "B atomic file", "C atomic file + nudge", "D dynamic store", "2s", "1.5s", "900ms", "300ms"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if mac.nudges != 2 {
		t.Errorf("nudges = %d, want one per run of method C only", mac.nudges)
	}
	if entries, _ := os.ReadDir(mac.dir); len(entries) != 0 {
		t.Errorf("scratch files left behind: %v", entries)
	}
	if keys := mac.store.sortedKeys(); len(keys) != 0 {
		t.Errorf("scratch store entries left behind: %v", keys)
	}
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func TestProbeLogicStoreMethodWritesTheProductionFormUnderItsOwnServiceID(t *testing.T) {
	mac := newFakeMacForProbe(t)
	mac.delay = map[int]time.Duration{3: time.Second}
	env := mac.env(1)
	if err := env.storeWrite(scratchZone("1", 3, 0), true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"State:/Network/Service/com.runos.vpnprobe.1-3-0/IPv4",
		"State:/Network/Service/com.runos.vpnprobe.1-3-0/DNS",
	}
	if got := mac.store.setKeys(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("set = %v, want %v", got, want)
	}
	for _, key := range mac.store.setKeys() {
		if storeOwnKey.MatchString(key) {
			t.Errorf("%s would be swept by the daemon: the probe must use its own namespace", key)
		}
	}
}

func TestProbeLogicExtraMethodIsOptIn(t *testing.T) {
	mac := newFakeMacForProbe(t)
	mac.delay = map[int]time.Duration{0: time.Second, 1: time.Second, 2: time.Second, 3: time.Second, 4: time.Second}
	env := mac.env(1)
	if r, _ := runProbe(env); len(r) != 4 {
		t.Errorf("results = %d, want 4 without the extra method", len(r))
	}
	env = mac.env(1)
	env.extra = true
	if r, _ := runProbe(env); len(r) != 5 || !strings.Contains(probeTable(r), "E store, DNS entry only") {
		t.Errorf("results = %d, want 5 with the extra method", len(r))
	}
}

func TestProbeLogicRepublishesAnUnlistedZoneOnceAndReportsIt(t *testing.T) {
	mac := newFakeMacForProbe(t)
	for m := 0; m < 4; m++ {
		mac.delay[m] = 20 * time.Second // later than the nudge, earlier than giving up
	}
	results, err := runProbe(mac.env(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.republished || !r.listed {
			t.Errorf("result %+v, want listed after one re-publish", r)
		}
	}
	if mac.flushes < 8 {
		t.Errorf("flushes = %d, want the writes and the nudges to flush DNS", mac.flushes)
	}
}

func TestProbeLogicReportsAZoneThatNeverAppears(t *testing.T) {
	mac := newFakeMacForProbe(t)
	for m := 0; m < 4; m++ {
		mac.delay[m] = -1
	}
	results, err := runProbe(mac.env(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.listed {
			t.Errorf("result %+v, want unlisted", r)
		}
	}
	if !strings.Contains(probeTable(results), " -") {
		t.Error("the table must show a dash where there is no latency")
	}
}

func TestProbeLogicStopsAndCleansUpWhenInterrupted(t *testing.T) {
	mac := newFakeMacForProbe(t)
	for m := 0; m < 4; m++ {
		mac.delay[m] = -1
	}
	env := mac.env(5)
	env.made = nil
	calls := 0
	env.stopped = func() bool { calls++; return calls > 40 } // Ctrl-C part way through the first zone
	results, _ := runProbe(env)
	env.cleanup()

	if len(results) > 1 {
		t.Errorf("results = %d, the run must stop soon after the interrupt", len(results))
	}
	if entries, _ := os.ReadDir(mac.dir); len(entries) != 0 {
		t.Errorf("scratch files left behind: %v", entries)
	}
	if keys := mac.store.sortedKeys(); len(keys) != 0 {
		t.Errorf("scratch store entries left behind: %v", keys)
	}
}

// The safety rules: only scratch zones and keys, and only files that carry the marker.
func TestProbeLogicRefusesToTouchWhatIsNotItsOwn(t *testing.T) {
	mac := newFakeMacForProbe(t)
	env := mac.env(1)

	for _, zone := range []string{"corp.example", "api.example.test", "dnsprobe-1-0-0.example.test/../x", "dnsprobe-.example.com", "dnsprobe-x.example.test"} {
		if err := env.guard(zone); err == nil {
			t.Errorf("guard accepted %q", zone)
		}
		if err := env.storeWrite(zone, true); err == nil {
			t.Errorf("storeWrite accepted %q", zone)
		}
	}
	for _, key := range []string{
		"State:/Network/Service/com.runos.vpn.192-0-2-53/DNS", // the daemon's own entry
		"State:/Network/Service/com.runos.vpnprobe.1-3-0/Proxies",
		"State:/Network/Service/ABCDEF/DNS",
		"Setup:/Network/Service/com.runos.vpnprobe.1-3-0/DNS",
	} {
		if err := guardStore(key); err == nil {
			t.Errorf("guardStore accepted %q", key)
		}
	}

	// A file without the marker is left alone by cleanup; a marked scratch file goes.
	scratch := scratchZone("7", 0, 0)
	foreign := filepath.Join(mac.dir, scratch)
	if err := os.WriteFile(foreign, []byte("nameserver 198.51.100.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.guard(scratch); err == nil {
		t.Error("guard accepted a file without the marker")
	}
	env.made = []probeMade{{0, scratch}}
	env.cleanup()
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("cleanup removed a file without the marker: %v", err)
	}
	ours := scratchZone("7", 0, 1)
	if err := os.WriteFile(filepath.Join(mac.dir, ours), []byte(resolverMarker+"\nnameserver "+probeNameserver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.made = []probeMade{{0, ours}}
	env.cleanup()
	if _, err := os.Stat(filepath.Join(mac.dir, ours)); !os.IsNotExist(err) {
		t.Error("cleanup should remove a scratch file that carries the marker")
	}

	// Cleanup never removes the daemon's own store entries, even if it is told to.
	mac.store.seed("State:/Network/Service/com.runos.vpn.192-0-2-53/DNS", map[string][]string{"ServerAddresses": {"x"}})
	env.made = []probeMade{{3, scratchZone("7", 3, 0)}}
	env.cleanup()
	if !mac.store.has("State:/Network/Service/com.runos.vpn.192-0-2-53/DNS") {
		t.Error("cleanup removed the daemon's store entry")
	}
}

func TestProbeLogicPreflightRefusesWhileTheVPNIsUp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(m *fakeMacForProbe)
		wantErr string
	}{
		{"a clean machine", func(m *fakeMacForProbe) {}, ""},
		{"the daemon's store entry is present", func(m *fakeMacForProbe) {
			m.store.seed("State:/Network/Service/com.runos.vpn.192-0-2-53/DNS", map[string][]string{"ServerAddresses": {"x"}})
		}, "turn the VPN off"},
		{"the daemon's resolver file is present", func(m *fakeMacForProbe) {
			_ = os.WriteFile(filepath.Join(m.dir, "real.example.test"), []byte(resolverMarker+"\nnameserver 192.0.2.9\n"), 0o644)
		}, "turn the VPN off"},
		{"a hand-authored file is not the daemon's", func(m *fakeMacForProbe) {
			_ = os.WriteFile(filepath.Join(m.dir, "corp.example"), []byte("nameserver 198.51.100.1\n"), 0o644)
		}, ""},
		{"scutil cannot be reached", func(m *fakeMacForProbe) { m.store.err = errors.New("no scutil") }, "cannot read the dynamic store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mac := newFakeMacForProbe(t)
			tc.arrange(mac)
			err := mac.env(1).preflight()
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestProbeLogicSweepsWhatAKilledRunLeftBehind(t *testing.T) {
	mac := newFakeMacForProbe(t)
	env := mac.env(1)
	left := scratchZone("99", 1, 0)
	_ = os.WriteFile(filepath.Join(mac.dir, left), []byte(resolverMarker+"\nnameserver "+probeNameserver+"\n"), 0o644)
	if err := env.storeWrite(scratchZone("99", 3, 0), true); err != nil {
		t.Fatal(err)
	}
	mac.store.seed("State:/Network/Service/com.runos.vpn.192-0-2-53/DNS", map[string][]string{"ServerAddresses": {"x"}})

	env.sweepLeftovers()

	if _, err := os.Stat(filepath.Join(mac.dir, left)); !os.IsNotExist(err) {
		t.Error("a leftover scratch file should be removed")
	}
	if keys := mac.store.sortedKeys(); len(keys) != 1 || !strings.Contains(keys[0], "com.runos.vpn.192-0-2-53") {
		t.Errorf("keys = %v, want only the daemon's own entry left alone", keys)
	}
}

func TestProbeLogicStopsWhenAMethodCannotWrite(t *testing.T) {
	mac := newFakeMacForProbe(t)
	mac.store.setErr = errors.New("exit status 1")
	mac.delay = map[int]time.Duration{0: time.Second, 1: time.Second, 2: time.Second}
	if _, err := runProbe(mac.env(1)); err == nil {
		t.Error("a write that fails must stop the probe with the reason")
	}
}

func TestProbeRefusesUnlessStartedBySudoFromTheMakeTarget(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, tc := range []struct {
		name    string
		getenv  func(string) string
		euid    int
		wantErr string
	}{
		{"no opt-in", env(), 0, "opt-in"},
		{"opted in but not through make", env("RUNOS_DNS_PROBE", "1", "SUDO_USER", "me"), 0, "make dnsprobe"},
		{"not root", env("RUNOS_DNS_PROBE", "1", "RUNOS_DNS_PROBE_VIA", "make", "SUDO_USER", "me"), 501, "sudo"},
		{"root but not through sudo", env("RUNOS_DNS_PROBE", "1", "RUNOS_DNS_PROBE_VIA", "make"), 0, "sudo"},
		{"the make target under sudo", env("RUNOS_DNS_PROBE", "1", "RUNOS_DNS_PROBE_VIA", "make", "SUDO_USER", "me"), 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := probeAllowed(tc.getenv, tc.euid)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
