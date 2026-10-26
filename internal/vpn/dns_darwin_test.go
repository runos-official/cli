//go:build darwin

package vpn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// scutilText renders what `scutil --dns` prints for the given zone -> nameserver pairs, in the
// shape macOS uses, plus the default resolver that is always there.
func scutilText(listed map[string]string) string {
	var b strings.Builder
	b.WriteString("DNS configuration\n\nresolver #1\n  nameserver[0] : 198.51.100.1\n  flags    : Request A records\n\n")
	zones := make([]string, 0, len(listed))
	for z := range listed {
		zones = append(zones, z)
	}
	sort.Strings(zones)
	for i, z := range zones {
		fmt.Fprintf(&b, "resolver #%d\n  domain   : %s\n  nameserver[0] : %s\n  flags    : Request A records\n\n", i+2, z, listed[z])
	}
	return b.String()
}

// countingResolverFS counts the writes that reach the file system.
type countingResolverFS struct {
	osResolverFS
	renames int
}

func (c *countingResolverFS) Rename(oldPath, newPath string) error {
	c.renames++
	return c.osResolverFS.Rename(oldPath, newPath)
}

// macWithScutil is a platform whose view of macOS DNS is the map the test edits.
type macWithScutil struct {
	darwinPlatform
	flushes *int
	fs      *countingResolverFS
	listed  map[string]string
	readErr error
}

func newMacWithScutil(t *testing.T) *macWithScutil {
	t.Helper()
	m := &macWithScutil{fs: &countingResolverFS{}, listed: map[string]string{}}
	p, flushes := testDarwinPlatform(t)
	m.darwinPlatform, m.flushes = p, flushes
	m.darwinPlatform.fs = m.fs
	m.darwinPlatform.store = noStore
	m.darwinPlatform.scutil = func() (string, error) {
		if m.readErr != nil {
			return "", m.readErr
		}
		return scutilText(m.listed), nil
	}
	return m
}

func plans(pairs ...string) []ResolverPlan {
	var out []ResolverPlan
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ResolverPlan{Zone: pairs[i], Resolver: mustAddr(pairs[i+1])})
	}
	return out
}

// One look after the write: ready, pending or a fault. The look never waits.
func TestReconcileDNSReportsWhatMacOSListsRightNow(t *testing.T) {
	for _, tc := range []struct {
		name        string
		listed      map[string]string
		readErr     error
		plan        []ResolverPlan
		onDisk      bool // our file for the plan is already there, so this look writes nothing
		wantReady   bool
		wantPending bool
		wantErr     string
	}{
		{"listed at once", map[string]string{"a.example.test": "192.0.2.53"}, nil, plans("a.example.test", "192.0.2.53"), false, true, false, ""},
		{"written but not listed yet", nil, nil, plans("a.example.test", "192.0.2.53"), false, false, true, ""},
		{"one of two zones listed", map[string]string{"a.example.test": "192.0.2.53"}, nil,
			plans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.54"), false, false, true, ""},
		{"another resolver answers for the zone", map[string]string{"a.example.test": "198.51.100.9"}, nil,
			plans("a.example.test", "192.0.2.53"), true, false, false, "overridden"},
		{"the Mac's DNS view cannot be read: unverified, not failed", nil, errors.New("scutil: exit status 1"),
			plans("a.example.test", "192.0.2.53"), false, false, false, ""},
		{"no zone wanted", nil, nil, nil, false, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMacWithScutil(t)
			m.listed, m.readErr = tc.listed, tc.readErr
			if tc.onDisk {
				for _, plan := range tc.plan {
					writeResolverFile(t, m.dir, plan.Zone, resolverMarker+"\nnameserver "+plan.Resolver.String()+"\n")
				}
			}

			status, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), tc.plan)

			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if status.Available != tc.wantReady || status.Pending != tc.wantPending {
				t.Errorf("available=%v pending=%v, want %v and %v (%+v)", status.Available, status.Pending, tc.wantReady, tc.wantPending, status)
			}
		})
	}
}

func TestPendingAndUnreadableTextIsPlain(t *testing.T) {
	m := newMacWithScutil(t)
	pending, _ := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))
	m.readErr = errors.New("scutil: signal: killed")
	unreadable, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))
	if err != nil {
		t.Fatalf("a DNS view that cannot be read is unverified, not a failed connect: %v", err)
	}
	if unreadable.Available || unreadable.Pending {
		t.Errorf("status = %+v, want neither available nor pending: nothing was verified", unreadable)
	}
	assertPlainText(t, "pending", pending.Error)
	assertPlainText(t, "unreadable", unreadable.Error)
}

// Idempotence: a normal reconcile of an unchanged plan writes nothing and does not flush.
func TestReconcileDNSWritesOnlyWhatChanged(t *testing.T) {
	m := newMacWithScutil(t)
	zoneA, zoneB := "a.example.test", "b.example.test"
	foreign := writeResolverFile(t, m.dir, "corp.example", "nameserver 198.51.100.1\n")
	m.listed = map[string]string{zoneA: "192.0.2.53", zoneB: "192.0.2.54"}

	steps := []struct {
		name        string
		plan        []ResolverPlan
		wantRenames int
		wantFlushes int
		wantFiles   []string
		listed      map[string]string // what macOS lists before this step; nil keeps the last
	}{
		{"first connect writes both", plans(zoneA, "192.0.2.53", zoneB, "192.0.2.54"), 2, 1, []string{zoneA, zoneB}, nil},
		{"unchanged plan writes nothing", plans(zoneA, "192.0.2.53", zoneB, "192.0.2.54"), 0, 0, []string{zoneA, zoneB}, nil},
		// macOS still lists the old address right after the rewrite: a delay, not an override.
		{"a changed resolver address rewrites that zone only", plans(zoneA, "192.0.2.99", zoneB, "192.0.2.54"), 1, 1, []string{zoneA, zoneB}, nil},
		{"a removed zone deletes only our file", plans(zoneA, "192.0.2.99"), 0, 1, []string{zoneA}, map[string]string{zoneA: "192.0.2.99"}},
		{"no zones removes the rest, never the foreign file", nil, 0, 1, nil, nil},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			m.fs.renames, *m.flushes = 0, 0
			if step.listed != nil {
				m.listed = step.listed
			}
			status, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), step.plan)
			if err != nil {
				t.Fatal(err)
			}
			if step.name == "a changed resolver address rewrites that zone only" && !status.Pending {
				t.Errorf("a zone macOS still lists with the old address right after a rewrite is pending, got %+v", status)
			}
			if m.fs.renames != step.wantRenames || *m.flushes != step.wantFlushes {
				t.Errorf("renames=%d flushes=%d, want %d and %d", m.fs.renames, *m.flushes, step.wantRenames, step.wantFlushes)
			}
			var ours []string
			for _, n := range names(t, m.dir) {
				if n != "corp.example" {
					ours = append(ours, n)
				}
			}
			if strings.Join(ours, ",") != strings.Join(step.wantFiles, ",") {
				t.Errorf("our files = %v, want %v", ours, step.wantFiles)
			}
			if _, err := os.Stat(foreign); err != nil {
				t.Errorf("the hand-authored file must stay: %v", err)
			}
		})
	}
}

// RecheckDNS repairs a missing file at once, and re-publishes an unlisted zone only when asked.
func TestRecheckDNSRepublishesOnlyTheUnlistedZones(t *testing.T) {
	zoneA, zoneB := "a.example.test", "b.example.test"
	want := plans(zoneA, "192.0.2.53", zoneB, "192.0.2.54")
	for _, tc := range []struct {
		name        string
		listed      map[string]string
		republish   bool
		deleteFile  string
		wantRenames int
		wantFlushes int
	}{
		{"not asked: no rewrite", map[string]string{zoneA: "192.0.2.53"}, false, "", 0, 0},
		{"asked: only the unlisted zone is rewritten", map[string]string{zoneA: "192.0.2.53"}, true, "", 1, 1},
		{"asked and both unlisted: both rewritten, one flush", nil, true, "", 2, 1},
		{"asked but everything listed: nothing to do", map[string]string{zoneA: "192.0.2.53", zoneB: "192.0.2.54"}, true, "", 0, 0},
		{"a deleted file is restored without being asked", map[string]string{zoneA: "192.0.2.53"}, false, zoneB, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMacWithScutil(t)
			m.listed = map[string]string{zoneA: "192.0.2.53", zoneB: "192.0.2.54"}
			if _, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), want); err != nil {
				t.Fatal(err)
			}
			if tc.deleteFile != "" {
				if err := os.Remove(filepath.Join(m.dir, tc.deleteFile)); err != nil {
					t.Fatal(err)
				}
			}
			m.listed = tc.listed
			m.fs.renames, *m.flushes = 0, 0

			if _, err := m.RecheckDNS(want, tc.republish); err != nil {
				t.Fatal(err)
			}

			if m.fs.renames != tc.wantRenames || *m.flushes != tc.wantFlushes {
				t.Errorf("renames=%d flushes=%d, want %d and %d", m.fs.renames, *m.flushes, tc.wantRenames, tc.wantFlushes)
			}
		})
	}
}

// A republish that cannot write is an error, so a read-only resolver directory is not hidden behind
// "still being applied".
func TestRecheckDNSReportsAWriteFailure(t *testing.T) {
	m := newMacWithScutil(t)
	if _, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53")); err != nil {
		t.Fatal(err)
	}
	m.darwinPlatform.fs = failingResolverFS{failAt: "rename"}

	status, err := m.RecheckDNS(plans("a.example.test", "192.0.2.53"), true)

	if err == nil || status.Pending || status.Available {
		t.Fatalf("status=%+v err=%v, want an error and no pending state", status, err)
	}
}

// The same old listing is a real override once the zone was NOT just rewritten: a look with nothing
// to write reports it.
func TestAnOverrideThatPersistsWithoutARewriteIsReported(t *testing.T) {
	m := newMacWithScutil(t)
	m.listed = map[string]string{"a.example.test": "192.0.2.53"}
	plan := plans("a.example.test", "192.0.2.99")
	if _, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plan); err != nil {
		t.Fatal(err)
	}

	status, err := m.RecheckDNS(plan, false)

	if err == nil || !strings.Contains(err.Error(), "overridden") {
		t.Fatalf("status=%+v err=%v, want the override reported on the second look", status, err)
	}
}
