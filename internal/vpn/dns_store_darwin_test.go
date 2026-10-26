//go:build darwin

package vpn

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

/*
The platform side of the dynamic store: publish, repair, remove, and prove it never touches a key
that is not ours. fakeScutilStore plays scutil; nothing here starts a process.
*/

const (
	fakeUUIDKey    = "State:/Network/Service/25B1EF23-4C7A-446A-B3FF-FCA0E4B011A3/DNS"
	fakeOtherKey   = "State:/Network/Service/com.example.other/DNS"
	fakeLookalike  = "State:/Network/Service/com.runos.vpnx/DNS"
	fakeSetupKey   = "Setup:/Network/Service/com.runos.vpn.192-0-2-53/DNS"
	storeKeyDNS53  = "State:/Network/Service/com.runos.vpn.192-0-2-53/DNS"
	storeKeyIPv453 = "State:/Network/Service/com.runos.vpn.192-0-2-53/IPv4"
	storeKeyDNS54  = "State:/Network/Service/com.runos.vpn.192-0-2-54/DNS"
	storeKeyIPv454 = "State:/Network/Service/com.runos.vpn.192-0-2-54/IPv4"
)

// seedForeignKeys puts keys in the store that belong to other software, or merely look like ours.
func seedForeignKeys(s *fakeScutilStore) {
	foreign := map[string][]string{"ServerAddresses": {"<array> {", "  0 : 198.51.100.1", "}"}}
	for _, k := range []string{fakeUUIDKey, fakeOtherKey, fakeLookalike, fakeSetupKey} {
		s.seed(k, foreign)
	}
}

func assertForeignKeysStay(t *testing.T, s *fakeScutilStore) {
	t.Helper()
	for _, k := range []string{fakeUUIDKey, fakeOtherKey, fakeLookalike, fakeSetupKey} {
		if !s.has(k) {
			t.Errorf("foreign key %s was removed", k)
		}
	}
	for _, k := range append(s.setKeys(), s.removedKeys()...) {
		for _, foreign := range []string{fakeUUIDKey, fakeOtherKey, fakeLookalike, fakeSetupKey} {
			if k == foreign {
				t.Errorf("a command named the foreign key %s", k)
			}
		}
	}
}

func storePlatform(t *testing.T) (darwinPlatform, *fakeScutilStore, *int) {
	t.Helper()
	p, flushes := testDarwinPlatform(t)
	store := newFakeScutilStore()
	p.store = store.run
	return p, store, flushes
}

func TestEnsureStorePublishesOnceAndSkipsWhatMatches(t *testing.T) {
	p, store, _ := storePlatform(t)
	plan := plans("a.example.test", "192.0.2.53")

	changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, false)
	if err != nil || !changed {
		t.Fatalf("first publish: changed=%v err=%v", changed, err)
	}
	if want := []string{storeKeyIPv453, storeKeyDNS53}; !reflect.DeepEqual(store.setKeys(), want) {
		t.Errorf("set = %v, want %v (IPv4 before DNS)", store.setKeys(), want)
	}

	store.log = nil
	changed, err = p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, false)
	if err != nil || changed || len(store.log) != 0 {
		t.Errorf("unchanged plan: changed=%v err=%v commands=%v, want nothing written", changed, err, store.log)
	}

	changed, err = p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, true)
	if err != nil || !changed || len(store.setKeys()) != 2 {
		t.Errorf("republish: changed=%v err=%v set=%v, want the entry set again", changed, err, store.setKeys())
	}
}

func TestEnsureStoreRepairsADeletedEntryAndAChangedOne(t *testing.T) {
	p, store, _ := storePlatform(t)
	plan := plans("a.example.test", "192.0.2.53")
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, false); err != nil {
		t.Fatal(err)
	}

	delete(store.keys, storeKeyDNS53) // another program removed our DNS entry
	store.log = nil
	if changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, false); err != nil || !changed || !store.has(storeKeyDNS53) {
		t.Errorf("a deleted entry: changed=%v err=%v present=%v, want it put back", changed, err, store.has(storeKeyDNS53))
	}

	store.log = nil
	more := plans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.53")
	if changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), more, false); err != nil || !changed {
		t.Fatalf("a new zone: changed=%v err=%v", changed, err)
	}
	out, _ := store.run("show " + storeKeyDNS53 + "\n")
	if entry, _ := parseStoreDNS(out); !reflect.DeepEqual(entry.Zones, []string{"a.example.test", "b.example.test"}) {
		t.Errorf("zones in the store = %v", entry.Zones)
	}
}

func TestEnsureStoreRemovesAServiceThePlanDropped(t *testing.T) {
	p, store, _ := storePlatform(t)
	seedForeignKeys(store)
	both := plans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.54")
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), both, false); err != nil {
		t.Fatal(err)
	}
	store.log = nil

	changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53"), false)

	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if want := []string{storeKeyDNS54, storeKeyIPv454}; !reflect.DeepEqual(store.removedKeys(), want) {
		t.Errorf("removed = %v, want %v", store.removedKeys(), want)
	}
	if !store.has(storeKeyDNS53) || !store.has(storeKeyIPv453) {
		t.Error("the service the plan still has must stay")
	}
	assertForeignKeysStay(t, store)
}

func TestEnsureStoreWithNoZonesOnlyRemoves(t *testing.T) {
	p, store, _ := storePlatform(t)
	seedForeignKeys(store)
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53"), false); err != nil {
		t.Fatal(err)
	}
	store.log = nil

	changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), nil, false)

	if err != nil || !changed || len(store.setKeys()) != 0 {
		t.Errorf("changed=%v err=%v set=%v, want a removal and no write", changed, err, store.setKeys())
	}
	if want := []string{storeKeyDNS53, storeKeyIPv453}; !reflect.DeepEqual(store.removedKeys(), want) {
		t.Errorf("removed = %v, want %v", store.removedKeys(), want)
	}
	assertForeignKeysStay(t, store)
}

// scutil's exit status says nothing about a refused set, so the entry is read back.
func TestEnsureStoreTrustsTheReadBackNotTheExitStatus(t *testing.T) {
	p, store, _ := storePlatform(t)
	store.ignoreSet = true // "Permission denied" is printed, the status is 0, nothing is stored

	changed, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53"), false)

	if err == nil || changed {
		t.Fatalf("changed=%v err=%v, want an error: the entry is not in the store", changed, err)
	}
}

func TestEnsureStoreReportsScutilFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeScutilStore)
	}{
		{"scutil is missing", func(s *fakeScutilStore) { s.err = errors.New(`exec: "scutil": executable file not found in $PATH`) }},
		{"scutil refuses the write", func(s *fakeScutilStore) { s.setErr = errors.New("exit status 1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, store, _ := storePlatform(t)
			tc.setup(store)
			if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53"), false); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestEnsureStoreRefusesAZoneThatCouldInjectACommandWithoutRunningScutil(t *testing.T) {
	p, store, _ := storePlatform(t)
	_, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test\nremove State:/Network/Service/x/DNS", "192.0.2.53"), false)
	if err == nil || store.runs != 0 {
		t.Errorf("err=%v runs=%d, want a refusal before any scutil run", err, store.runs)
	}
}

// The sweep that runs on every daemon start, and Teardown for `down`: exactly our keys go.
func TestTeardownRemovesExactlyOurStoreKeys(t *testing.T) {
	p, store, flushes := storePlatform(t)
	seedForeignKeys(store)
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"),
		plans("a.example.test", "192.0.2.53", "b.example.test", "192.0.2.54"), false); err != nil {
		t.Fatal(err)
	}
	store.log = nil

	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}

	want := []string{storeKeyDNS53, storeKeyIPv453, storeKeyDNS54, storeKeyIPv454}
	if !reflect.DeepEqual(store.removedKeys(), want) {
		t.Errorf("removed = %v, want %v", store.removedKeys(), want)
	}
	assertForeignKeysStay(t, store)
	if *flushes != 1 {
		t.Errorf("flushes = %d, want 1: something was removed", *flushes)
	}

	// A second teardown (a restart that finds nothing) removes nothing and does not flush.
	store.log = nil
	if err := p.Teardown("utun9"); err != nil || len(store.log) != 0 || *flushes != 1 {
		t.Errorf("second teardown: err=%v commands=%v flushes=%d, want nothing", err, store.log, *flushes)
	}
}

func TestTeardownLeavesAStoreWithOnlyForeignKeysAlone(t *testing.T) {
	p, store, flushes := storePlatform(t)
	seedForeignKeys(store)
	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}
	if len(store.log) != 0 || *flushes != 0 {
		t.Errorf("commands=%v flushes=%d, want none", store.log, *flushes)
	}
	assertForeignKeysStay(t, store)
}

// A key that cannot be removed is an error the caller logs, and the sweep still removes the rest of
// what it can reach (the next start tries again).
func TestTeardownReportsAKeyThatStaysAndMachineIsNotCalledClean(t *testing.T) {
	p, store, _ := storePlatform(t)
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plans("a.example.test", "192.0.2.53"), false); err != nil {
		t.Fatal(err)
	}
	store.ignoreRemove = true

	err := p.Teardown("utun9")

	if err == nil || !strings.Contains(err.Error(), "still") {
		t.Errorf("err = %v, want a report that keys are still in the store", err)
	}
}

func TestTeardownWithoutScutilIsAnErrorForTheLogNotAPanic(t *testing.T) {
	p, store, _ := storePlatform(t)
	store.err = errors.New("exec: scutil: not found")
	if err := p.Teardown("utun9"); err == nil {
		t.Error("want the store sweep failure reported")
	}
}

// The IPv4 entry carries the interface and the address. A new tunnel interface or a new device address
// has to reach the store, not only a changed DNS entry.
func TestEnsureStoreRepairsAChangedIPv4Entry(t *testing.T) {
	p, store, _ := storePlatform(t)
	plan := plans("a.example.test", "192.0.2.53")
	if _, err := p.ensureStore("utun9", mustAddr("192.0.2.2"), plan, false); err != nil {
		t.Fatal(err)
	}
	store.log = nil

	changed, err := p.ensureStore("utun10", mustAddr("192.0.2.2"), plan, false)
	if err != nil || !changed {
		t.Fatalf("a new interface: changed=%v err=%v, want the entry rewritten", changed, err)
	}
	changed, err = p.ensureStore("utun10", mustAddr("192.0.2.7"), plan, false)
	if err != nil || !changed {
		t.Fatalf("a new address: changed=%v err=%v, want the entry rewritten", changed, err)
	}
	store.log = nil
	if changed, err := p.ensureStore("utun10", mustAddr("192.0.2.7"), plan, false); err != nil || changed || len(store.log) != 0 {
		t.Errorf("nothing changed: changed=%v err=%v commands=%v", changed, err, store.log)
	}
}
