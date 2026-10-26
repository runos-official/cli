//go:build darwin

package vpn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

/*
The fallback chain (FCR 1730): the dynamic-store entry is the primary mechanism, the resolver file the
second, and the answer comes from what `scutil --dns` lists.

chainMac models the Mac. Each mechanism has its own switch for "macOS picks this up": the store entry
and our resolver files are rendered into scutil --dns text only when the switch is on, so a test can
say "the store works, the file door is silent" (what the measurements suggest) or the reverse.
*/
type chainMac struct {
	darwinPlatform
	store   *fakeScutilStore
	flushes *int
	fs      *countingResolverFS
	// storePickup and filePickup say whether macOS lists the zones of that mechanism.
	storePickup, filePickup bool
	// extra is raw resolver text appended to the listing: another program's resolver for a zone.
	extra string
	// dnsErr makes `scutil --dns` fail, as a missing or broken scutil does.
	dnsErr error
}

func newChainMac(t *testing.T) *chainMac {
	t.Helper()
	p, flushes := testDarwinPlatform(t)
	m := &chainMac{store: newFakeScutilStore(), flushes: flushes, fs: &countingResolverFS{}, storePickup: true, filePickup: true}
	m.darwinPlatform = p
	m.darwinPlatform.store = m.store.run
	m.darwinPlatform.fs = m.fs
	m.darwinPlatform.scutil = m.dnsText
	return m
}

// dnsText renders what `scutil --dns` would print. It follows the shape of the macOS 26 listing: a
// default resolver first, then one block per match domain, supplemental ones flagged and ordered.
func (m *chainMac) dnsText() (string, error) {
	if m.dnsErr != nil {
		return "", m.dnsErr
	}
	var b strings.Builder
	b.WriteString("DNS configuration\n\nresolver #1\n  nameserver[0] : 198.51.100.1\n  if_index : 18 (en7)\n  flags    : Request A records\n  reach    : 0x00020002 (Reachable,Directly Reachable Address)\n\n")
	n := 2
	block := func(zone, server, flags string, order int) {
		fmt.Fprintf(&b, "resolver #%d\n  domain   : %s\n  nameserver[0] : %s\n  flags    : %s\n  reach    : 0x00000002 (Reachable)\n  order    : %d\n\n", n, zone, server, flags, order)
		n++
	}
	if m.storePickup {
		for _, key := range m.store.sortedKeys() {
			if !strings.HasSuffix(key, "/DNS") || !strings.Contains(key, "com.runos.vpn") {
				continue
			}
			out, _ := m.store.run("show " + key + "\n")
			entry, _ := parseStoreDNS(out)
			for i, zone := range entry.Zones {
				block(zone, entry.Servers[0].String(), "Supplemental, Request A records", 100000+200*i)
			}
		}
	}
	if m.filePickup {
		entries, _ := os.ReadDir(m.dir)
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(m.dir, e.Name()))
			if addr, ok := parseResolverNameserver(data); ok && isManagedResolverFile(data) {
				block(e.Name(), addr.String(), "Request A records", 1)
			}
		}
	}
	b.WriteString(m.extra)
	return b.String(), nil
}

func (m *chainMac) reconcile(plan []ResolverPlan) (DNSStatus, error) {
	return m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plan)
}

func (m *chainMac) recheck(plan []ResolverPlan, republish bool) (DNSStatus, error) {
	return m.RecheckDNS("utun9", mustAddr("192.0.2.2"), plan, republish)
}

func (m *chainMac) fileExists(zone string) bool {
	_, err := os.Stat(filepath.Join(m.dir, zone))
	return err == nil
}

var chainPlan = plans("a.example.test", "192.0.2.53")

// Both mechanisms are written and the store is named as the one in charge.
func TestChainWritesTheStoreFirstAndTheFileToo(t *testing.T) {
	m := newChainMac(t)

	status, err := m.reconcile(chainPlan)

	if err != nil || !status.Available || status.Mode != "store" {
		t.Fatalf("status=%+v err=%v, want ready in store mode", status, err)
	}
	if !m.store.has(storeKeyDNS53) || !m.fileExists("a.example.test") {
		t.Errorf("store entry: %v, file: %v; want both in place", m.store.has(storeKeyDNS53), m.fileExists("a.example.test"))
	}
	if *m.flushes != 1 {
		t.Errorf("flushes = %d, want one after the writes", *m.flushes)
	}
}

func TestChainFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		arrange     func(m *chainMac)
		wantReady   bool
		wantPending bool
		wantMode    string
		wantErr     bool
		wantText    string // DNSStatus.Error must be exactly this when set
	}{
		{
			name:      "store works, the file cannot be written: ready through the store",
			arrange:   func(m *chainMac) { m.darwinPlatform.fs = failingResolverFS{failAt: "rename"} },
			wantReady: true, wantMode: "store",
		},
		{
			name:      "store fails, the file works and macOS lists it: ready through the file",
			arrange:   func(m *chainMac) { m.darwinPlatform.store = noStore },
			wantReady: true, wantMode: "native",
		},
		{
			name:        "store fails, the file is written but macOS does not list it: pending",
			arrange:     func(m *chainMac) { m.darwinPlatform.store = noStore; m.filePickup = false },
			wantPending: true, wantMode: "native",
		},
		{
			name:        "store written but macOS does not list it, file silent too: pending, store in charge",
			arrange:     func(m *chainMac) { m.storePickup, m.filePickup = false, false },
			wantPending: true, wantMode: "store",
		},
		{
			name: "both cannot be written: a plain error",
			arrange: func(m *chainMac) {
				m.darwinPlatform.store = noStore
				m.darwinPlatform.fs = failingResolverFS{failAt: "rename"}
			},
			wantErr: true, wantMode: "unavailable", wantText: dnsWriteText,
		},
		{
			name: "scutil is missing: the store fails and the listing cannot be read, so it is unverified, not failed",
			arrange: func(m *chainMac) {
				m.darwinPlatform.store = noStore
				m.dnsErr = errors.New(`exec: "scutil": executable file not found in $PATH`)
			},
			wantMode: "unavailable", wantText: dnsUnreadableText,
		},
		{
			name:     "scutil answers the store but the listing cannot be read: unverified, not failed",
			arrange:  func(m *chainMac) { m.dnsErr = errors.New("scutil: exit status 1") },
			wantMode: "unavailable", wantText: dnsUnreadableText,
		},
		{
			name:    "the store takes the write but keeps nothing: an error from the store alone is not a failure while the file works",
			arrange: func(m *chainMac) { m.store.ignoreSet = true },
			// The file works and macOS lists it.
			wantReady: true, wantMode: "native",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChainMac(t)
			tc.arrange(m)

			status, err := m.reconcile(chainPlan)

			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if status.Available != tc.wantReady || status.Pending != tc.wantPending || status.Mode != tc.wantMode {
				t.Errorf("status = %+v, want ready=%v pending=%v mode=%s", status, tc.wantReady, tc.wantPending, tc.wantMode)
			}
			if tc.wantText != "" && status.Error != tc.wantText {
				t.Errorf("error text = %q, want %q", status.Error, tc.wantText)
			}
			if status.Error != "" {
				assertPlainText(t, "status error", status.Error)
			}
		})
	}
}

// A file RunOS wrote earlier with another address must not keep answering for a zone the store now
// serves. When the file cannot be rewritten, it is removed: the store wins.
func TestChainStoreWinsOverAStaleFileOfOurOwn(t *testing.T) {
	m := newChainMac(t)
	writeResolverFile(t, m.dir, "a.example.test", resolverMarker+"\nnameserver 192.0.2.99\n")
	m.darwinPlatform.fs = failingResolverFS{failAt: "rename"}

	status, err := m.reconcile(chainPlan)

	if err != nil || !status.Available || status.Mode != "store" {
		t.Fatalf("status=%+v err=%v, want ready through the store", status, err)
	}
	if m.fileExists("a.example.test") {
		t.Error("the stale file of our own must be removed when it cannot be rewritten")
	}
}

// A hand-authored file is never replaced or removed, and it does not stop the store.
func TestChainAHandAuthoredFileDoesNotStopTheStore(t *testing.T) {
	m := newChainMac(t)
	mine := writeResolverFile(t, m.dir, "a.example.test", "nameserver 198.51.100.1\n")

	status, err := m.reconcile(chainPlan)

	if err != nil || !status.Available || status.Mode != "store" {
		t.Fatalf("status=%+v err=%v, want ready through the store", status, err)
	}
	if got, _ := os.ReadFile(mine); string(got) != "nameserver 198.51.100.1\n" {
		t.Errorf("hand-authored file = %q, must be untouched", got)
	}
}

// Another resolver answering for the zone is a fault, and a look right after our own write is a delay.
func TestChainTreatsAForeignResolverAsADelayOnlyRightAfterAWrite(t *testing.T) {
	m := newChainMac(t)
	m.extra = "resolver #9\n  domain   : a.example.test\n  nameserver[0] : 198.51.100.9\n  flags    : Request A records\n  order    : 50000\n\n"

	first, err := m.reconcile(chainPlan)
	if err != nil || !first.Pending {
		t.Fatalf("first look after a write: status=%+v err=%v, want pending", first, err)
	}
	second, err := m.recheck(chainPlan, false)
	if err == nil || !strings.Contains(err.Error(), "overridden") {
		t.Fatalf("second look with nothing to write: status=%+v err=%v, want the override reported", second, err)
	}
}

// The watch repairs whichever mechanism is missing on every look, and rewrites both when asked.
func TestChainRecheckRepairsWhateverIsMissing(t *testing.T) {
	m := newChainMac(t)
	m.storePickup, m.filePickup = false, false
	if status, err := m.reconcile(chainPlan); err != nil || !status.Pending {
		t.Fatalf("setup: status=%+v err=%v", status, err)
	}

	delete(m.store.keys, storeKeyDNS53)
	if err := os.Remove(filepath.Join(m.dir, "a.example.test")); err != nil {
		t.Fatal(err)
	}
	m.store.log, m.fs.renames = nil, 0
	if _, err := m.recheck(chainPlan, false); err != nil {
		t.Fatal(err)
	}
	if !m.store.has(storeKeyDNS53) || !m.fileExists("a.example.test") {
		t.Errorf("after a look: store entry %v, file %v; want both put back", m.store.has(storeKeyDNS53), m.fileExists("a.example.test"))
	}

	m.storePickup = true
	status, err := m.recheck(chainPlan, false)
	if err != nil || !status.Available || status.Mode != "store" {
		t.Errorf("once macOS lists the zone: status=%+v err=%v, want ready", status, err)
	}
}

func TestChainRepublishResetsTheStoreEntryAndRewritesTheFile(t *testing.T) {
	m := newChainMac(t)
	m.storePickup, m.filePickup = false, false
	if _, err := m.reconcile(chainPlan); err != nil {
		t.Fatal(err)
	}
	m.store.log, m.fs.renames, *m.flushes = nil, 0, 0

	status, err := m.recheck(chainPlan, true)

	if err != nil || !status.Pending {
		t.Fatalf("status=%+v err=%v, want pending", status, err)
	}
	if want := []string{storeKeyDNS53, storeKeyIPv453}; !reflect.DeepEqual(m.store.removedKeys(), want) {
		t.Errorf("removed = %v, want the entry taken out and put back: %v", m.store.removedKeys(), want)
	}
	if want := []string{storeKeyIPv453, storeKeyDNS53}; !reflect.DeepEqual(m.store.setKeys(), want) {
		t.Errorf("set = %v, want %v", m.store.setKeys(), want)
	}
	if m.fs.renames != 1 || *m.flushes != 1 {
		t.Errorf("renames=%d flushes=%d, want the file rewritten once and one flush", m.fs.renames, *m.flushes)
	}
}

// With nothing wanted both mechanisms are cleared, other programs' entries are not touched.
func TestChainNoZonesClearsBothMechanisms(t *testing.T) {
	m := newChainMac(t)
	seedForeignKeys(m.store)
	foreign := writeResolverFile(t, m.dir, "corp.example", "nameserver 198.51.100.1\n")
	if _, err := m.reconcile(chainPlan); err != nil {
		t.Fatal(err)
	}

	status, err := m.reconcile(nil)

	if err != nil || status.Available {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if m.store.has(storeKeyDNS53) || m.fileExists("a.example.test") {
		t.Error("our store entry and our file must both be gone")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("the hand-authored file must stay: %v", err)
	}
	assertForeignKeysStay(t, m.store)
}

// Several clusters with different resolvers: one entry each, listed by macOS with their own server.
func TestChainTwoResolversTwoEntries(t *testing.T) {
	m := newChainMac(t)
	plan := plans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.54", "c.example.test", "192.0.2.54")

	status, err := m.reconcile(plan)

	if err != nil || !status.Available {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	keys := m.store.sortedKeys()
	sort.Strings(keys)
	if want := []string{storeKeyDNS53, storeKeyIPv453, storeKeyDNS54, storeKeyIPv454}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// What `runos vpn up` prints and Desktop shows never carries the tools' words.
func TestChainMessagesStayPlain(t *testing.T) {
	m := newChainMac(t)
	m.storePickup, m.filePickup = false, false
	pending, _ := m.reconcile(chainPlan)
	assertPlainText(t, "pending", pending.Error)
}
