//go:build darwin

package vpn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
The macOS resolver directory and what can go wrong with it.
*/

// A resolver file that RunOS did not write is the person's own. It is never replaced, whether the
// file is a plain nameserver line or has other settings, and the error says so in plain words.
func TestAHandAuthoredResolverFileIsNeverOverwritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"a plain nameserver line", "nameserver 198.51.100.1\n"},
		{"settings beyond the nameserver", "nameserver 198.51.100.1\nsearch corp.example\ntimeout 3\n"},
		{"the marker only as a lookalike", resolverMarker + "-dev\nnameserver 198.51.100.1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMacWithScutil(t)
			path := writeResolverFile(t, m.dir, "zone.example.test", tc.body)

			status, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))

			if got, _ := os.ReadFile(path); string(got) != tc.body {
				t.Fatalf("the hand-authored file was changed to %q", got)
			}
			if err == nil || status.Available {
				t.Fatalf("status=%+v err=%v, want a fault: the zone is not served by the tunnel", status, err)
			}
			assertPlainText(t, "hand-authored file", err.Error())
			if !strings.Contains(err.Error(), "not made by RunOS") {
				t.Errorf("error = %q, want it to say the file is not RunOS's", err)
			}
			if m.fs.renames != 0 {
				t.Error("a write reached the file system")
			}
		})
	}
}

// Removing our zone never removes the hand-authored file next to it (existing rule, kept).
func TestAHandAuthoredResolverFileIsNeverRemoved(t *testing.T) {
	m := newMacWithScutil(t)
	foreign := writeResolverFile(t, m.dir, "corp.example", "nameserver 198.51.100.1\n")
	if _, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53")); err != nil {
		t.Fatal(err)
	}

	if _, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Teardown("utun9"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("the hand-authored file is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.dir, "zone.example.test")); !os.IsNotExist(err) {
		t.Error("our own file should be removed")
	}
}

// A missing resolver directory is created, with the mode macOS expects.
func TestAMissingResolverDirectoryIsCreated(t *testing.T) {
	m := newMacWithScutil(t)
	m.dir = filepath.Join(t.TempDir(), "resolver")
	m.listed = map[string]string{"zone.example.test": "192.0.2.53"}

	status, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))

	if err != nil || !status.Available {
		t.Fatalf("status=%+v err=%v, want ready", status, err)
	}
	info, statErr := os.Stat(m.dir)
	if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Errorf("resolver directory = %v, %v, want a 0755 directory", info, statErr)
	}
}

// A resolver directory that cannot be written is a fault with a plain message. The system's own words
// (paths, the zone) go to the daemon log, not to the person.
func TestAnUnwritableResolverDirectoryIsAPlainFaultWithTheDetailInTheLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	m := newMacWithScutil(t)
	if err := os.Chmod(m.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(m.dir, 0o755) })

	var status DNSStatus
	var err error
	logged := captureLog(t, func() {
		status, err = m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))
	})

	if err == nil || status.Available || status.Pending {
		t.Fatalf("status=%+v err=%v, want a fault", status, err)
	}
	assertPlainText(t, "write fault", err.Error())
	assertPlainText(t, "write fault status", status.Error)
	if !strings.Contains(logged, "permission denied") {
		t.Errorf("the daemon log lacks the system's reason:\n%s", logged)
	}
	if leftovers := names(t, m.dir); len(leftovers) != 0 {
		t.Errorf("files left in the directory: %v", leftovers)
	}
}

// An empty file at the zone's path is what an older daemon left when a write was cut short (it
// truncated before it wrote). It is ours to replace, or the zone would stay broken for good.
func TestAnEmptyResolverFileLeftByACutShortWriteIsReplaced(t *testing.T) {
	m := newMacWithScutil(t)
	path := writeResolverFile(t, m.dir, "zone.example.test", "")
	m.listed = map[string]string{"zone.example.test": "192.0.2.53"}

	status, err := m.ReconcileDNS("utun9", mustAddr("192.0.2.2"), plans("zone.example.test", "192.0.2.53"))

	if err != nil || !status.Available {
		t.Fatalf("status=%+v err=%v, want ready", status, err)
	}
	if got, _ := os.ReadFile(path); !isManagedResolverFile(got) {
		t.Errorf("file = %q, want ours", got)
	}
}
