//go:build darwin

package vpn

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

/*
The resolver file is never visible empty or half written.

THEORY H1 (FCR 1730), not yet measured on a Mac: macOS watches /etc/resolver and may read a file at
the moment it appears. os.WriteFile opens with O_TRUNC and writes after, so a watcher that wakes on
the open event can read an empty file and cache that. Writing a temp file and renaming it into place
means only complete content is ever observable at the destination.

The reader below polls the destination as fast as it can while many writes run. Every successful read
must be a complete resolver file for one of the addresses being written.
*/
func TestResolverFileIsNeverObservedEmptyOrPartialWhileItIsRewritten(t *testing.T) {
	p, _ := testDarwinPlatform(t)
	zone := "zone.example.test"
	addrs := []string{"192.0.2.53", "192.0.2.54", "192.0.2.55"}
	valid := map[string]bool{}
	for _, a := range addrs {
		valid[resolverMarker+"\nnameserver "+a+"\n"] = true
	}
	dest := filepath.Join(p.dir, zone)
	writeResolverFile(t, p.dir, zone, resolverMarker+"\nnameserver "+addrs[0]+"\n")

	var stop atomic.Bool
	var wg sync.WaitGroup
	var bad atomic.Value
	var reads atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			data, err := os.ReadFile(dest)
			if err != nil {
				bad.CompareAndSwap(nil, fmt.Sprintf("read failed: %v", err))
				return
			}
			reads.Add(1)
			if !valid[string(data)] {
				bad.CompareAndSwap(nil, fmt.Sprintf("observed %q", data))
				return
			}
		}
	}()

	for i := 0; i < 3000 && bad.Load() == nil; i++ {
		if err := p.SetResolver(zone, mustAddr(addrs[i%len(addrs)])); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()

	if v := bad.Load(); v != nil {
		t.Fatalf("the destination was observable in a bad state after %d reads: %v", reads.Load(), v)
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// SetResolver leaves nothing but the destination: no temp file in the resolver directory or beside it.
func TestSetResolverLeavesNoTempFileBehind(t *testing.T) {
	p, _ := testDarwinPlatform(t)
	if err := p.SetResolver("zone.example.test", mustAddr("192.0.2.53")); err != nil {
		t.Fatal(err)
	}
	if got := names(t, p.dir); len(got) != 1 || got[0] != "zone.example.test" {
		t.Errorf("resolver directory = %v, want only the destination", got)
	}
	for _, n := range names(t, p.tempDir()) {
		if len(n) >= len(resolverTempPrefix) && n[:len(resolverTempPrefix)] == resolverTempPrefix {
			t.Errorf("temp file %s left beside the resolver directory", n)
		}
	}
}

// A write that fails midway leaves the existing file whole, so a failed re-publish never costs the
// zone the entry it already has.
func TestSetResolverFailureKeepsTheExistingFile(t *testing.T) {
	p, _ := testDarwinPlatform(t)
	p.fs = failingResolverFS{failAt: "rename"}
	path := writeResolverFile(t, p.dir, "zone.example.test", resolverMarker+"\nnameserver 192.0.2.53\n")

	if err := p.SetResolver("zone.example.test", mustAddr("192.0.2.99")); err == nil {
		t.Fatal("want the injected failure")
	}
	if got, _ := os.ReadFile(path); string(got) != resolverMarker+"\nnameserver 192.0.2.53\n" {
		t.Errorf("file = %q, want the previous content", got)
	}
}

func TestTeardownRemovesStaleTempFilesNextToTheResolverDirectory(t *testing.T) {
	p, _ := testDarwinPlatform(t)
	stale := filepath.Join(p.tempDir(), resolverTempPrefix+"42.tmp")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale temp file should be swept")
	}
}
