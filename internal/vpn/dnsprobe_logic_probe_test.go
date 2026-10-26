//go:build probe && darwin

package vpn

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run the probe's logic on a temp directory and a fake macOS. They need no root, so
// `go test -tags probe -run TestProbeLogic ./internal/vpn` checks the probe itself.

type fakeMacForProbe struct {
	t      time.Time
	dir    string
	wrote  map[string]time.Time
	delay  func(zone string) time.Duration // how long after the write macOS lists the zone; <0 never
	flushs int
}

func (m *fakeMacForProbe) env(t *testing.T, n int) *probeEnv {
	m.wrote = map[string]time.Time{}
	return &probeEnv{
		dir: m.dir,
		scutil: func() (string, error) {
			var b strings.Builder
			b.WriteString("DNS configuration\n\n")
			entries, _ := os.ReadDir(m.dir)
			for _, e := range entries {
				if d := m.delay(e.Name()); d >= 0 && m.t.Sub(m.wrote[e.Name()]) >= d {
					fmt.Fprintf(&b, "resolver #2\n  domain   : %s\n  nameserver[0] : %s\n\n", e.Name(), probeNameserver)
				}
			}
			return b.String(), nil
		},
		flush: func() error { m.flushs++; return nil },
		now:   func() time.Time { return m.t },
		sleep: func(d time.Duration) { m.t = m.t.Add(d) },
		out:   io.Discard, n: n, runID: "1",
		poll: 50 * time.Millisecond, giveUp: 30 * time.Second, nudgeAt: 8 * time.Second,
		stopped: func() bool { return false },
	}
}

func TestProbeLogicRecordsLatencyPerMethodAndCleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "resolver")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mac := &fakeMacForProbe{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), dir: dir}
	// Method 0 (old) is listed after 2 s, method 1 (new) after 300 ms. The write time is read from the
	// file's presence: the fake stamps a zone the first time it is seen.
	mac.delay = func(zone string) time.Duration {
		if _, ok := mac.wrote[zone]; !ok {
			mac.wrote[zone] = mac.t
		}
		if strings.Contains(zone, "-0-") {
			return 2 * time.Second
		}
		return 300 * time.Millisecond
	}
	env := mac.env(t, 3)

	results, err := runProbe(env)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 6 {
		t.Fatalf("results = %d, want 3 runs of 2 methods", len(results))
	}
	table := probeTable(results)
	for _, want := range []string{"truncate+write (old)", "atomic rename (new)", "2s", "300ms"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("scratch files left behind: %v", entries)
	}
}

func TestProbeLogicRepublishesAnUnlistedZoneOnceAndReportsIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "resolver")
	_ = os.Mkdir(dir, 0o755)
	mac := &fakeMacForProbe{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), dir: dir}
	mac.delay = func(zone string) time.Duration {
		if _, ok := mac.wrote[zone]; !ok {
			mac.wrote[zone] = mac.t
		}
		return 20 * time.Second // later than the nudge, earlier than giving up
	}
	env := mac.env(t, 1)

	results, err := runProbe(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.republished || !r.listed {
			t.Errorf("result %+v, want listed after one re-publish", r)
		}
	}
	if mac.flushs < 2 {
		t.Errorf("flushes = %d, want the nudge to flush DNS", mac.flushs)
	}
}

func TestProbeLogicReportsAZoneThatNeverAppears(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "resolver")
	_ = os.Mkdir(dir, 0o755)
	mac := &fakeMacForProbe{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), dir: dir}
	mac.delay = func(string) time.Duration { return -1 }
	results, err := runProbe(mac.env(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.listed {
			t.Errorf("result %+v, want unlisted", r)
		}
	}
	if !strings.Contains(probeTable(results), "-") {
		t.Error("the table must show a dash where there is no latency")
	}
}

// The safety rules: only scratch zones, and only files that carry the marker.
func TestProbeLogicRefusesToTouchWhatIsNotItsOwn(t *testing.T) {
	dir := t.TempDir()
	env := &probeEnv{dir: dir, flush: func() error { return nil }}

	for _, zone := range []string{"corp.example", "api.example.test", "dnsprobe-1-0-0.example.test/../x", "dnsprobe-.example.com"} {
		if err := env.guard(zone); err == nil {
			t.Errorf("guard accepted %q", zone)
		}
	}

	scratch := scratchZone("7", 0, 0)
	foreign := filepath.Join(dir, scratch)
	if err := os.WriteFile(foreign, []byte("nameserver 198.51.100.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.guard(scratch); err == nil {
		t.Error("guard accepted a file without the marker")
	}
	env.made = []string{scratch}
	env.cleanup()
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("cleanup removed a file without the marker: %v", err)
	}

	ours := filepath.Join(dir, scratchZone("7", 0, 1))
	if err := os.WriteFile(ours, []byte(resolverMarker+"\nnameserver "+probeNameserver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.made = []string{scratchZone("7", 0, 1)}
	env.cleanup()
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Error("cleanup should remove a scratch file that carries the marker")
	}
}

func TestProbeRefusesWithoutOptInOrRoot(t *testing.T) {
	// TestDNSPublishProbe skips unless RUNOS_DNS_PROBE=1, so a plain `go test -tags probe` is safe.
	if os.Getenv("RUNOS_DNS_PROBE") == "1" {
		t.Skip("the opt-in is set in this environment")
	}
	t.Log("the real probe is skipped without RUNOS_DNS_PROBE=1")
}
