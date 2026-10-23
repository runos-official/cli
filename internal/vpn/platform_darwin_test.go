//go:build darwin

package vpn

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests run the real macOS platform code against a temporary directory and a counting flush.
// They never touch /etc/resolver or the DNS cache.
func testDarwinPlatform(t *testing.T) (darwinPlatform, *int) {
	t.Helper()
	flushes := 0
	return darwinPlatform{dir: t.TempDir(), flush: func() error { flushes++; return nil }}, &flushes
}

func writeResolverFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

/*
The start-up sweep flushes the DNS cache only when it removed something.

Every daemon start runs Teardown. It flushed (dscacheutil plus a HUP to mDNSResponder) on every
call, so every start and every restart reloaded the machine's resolver for nothing.
*/
func TestTeardownDoesNotFlushWhenThereWasNothingToRemove(t *testing.T) {
	p, flushes := testDarwinPlatform(t)
	foreign := writeResolverFile(t, p.dir, "corp.example", "nameserver 198.51.100.1\n")

	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}

	if *flushes != 0 {
		t.Errorf("flushes = %d, want 0: nothing of ours was on the machine", *flushes)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file that is not ours was touched: %v", err)
	}
}

func TestTeardownRemovesOurFilesAndFlushesOnce(t *testing.T) {
	p, flushes := testDarwinPlatform(t)
	ours := writeResolverFile(t, p.dir, "a.example", resolverMarker+"\nnameserver 192.0.2.53\n")
	alsoOurs := writeResolverFile(t, p.dir, "b.example", resolverMarker+"\nnameserver 192.0.2.54\n")
	lookalike := writeResolverFile(t, p.dir, "c.example", resolverMarker+"-dev\nnameserver 192.0.2.55\n")
	foreign := writeResolverFile(t, p.dir, "d.example", "nameserver 198.51.100.1\n")

	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}

	for _, gone := range []string{ours, alsoOurs} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", gone)
		}
	}
	for _, kept := range []string{lookalike, foreign} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s must stay: %v", kept, err)
		}
	}
	if *flushes != 1 {
		t.Errorf("flushes = %d, want 1", *flushes)
	}
}

func TestTeardownOnAMissingResolverDirectoryIsQuiet(t *testing.T) {
	flushes := 0
	p := darwinPlatform{dir: filepath.Join(t.TempDir(), "absent"), flush: func() error { flushes++; return nil }}
	if err := p.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}
	if flushes != 0 {
		t.Errorf("flushes = %d, want 0", flushes)
	}
}
