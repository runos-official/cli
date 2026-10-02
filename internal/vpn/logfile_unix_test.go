//go:build !windows

package vpn

import (
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// primaryGroup is a group the test user belongs to, so a chown to it is allowed without root.
func primaryGroup(t *testing.T) (name string, gid int) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skip("no primary group entry")
	}
	id, ok := groupGID(g.Name)
	if !ok {
		t.Skip("group id is not numeric")
	}
	return g.Name, id
}

func gidOfFile(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return int(info.Sys().(*syscall.Stat_t).Gid)
}

// With no socket group configured this is a single-user machine and the log is world readable, so
// a stuck user can produce their own report without root.
func TestTheDaemonLogIsWorldReadableWhenNoSocketGroupIsConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 1024, "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("mode %v, want 0644 on a machine with no socket group", got)
	}
}

// On a shared host the log carries account, device and cluster ids and the failure text of other
// systems. It belongs to the people who may already control the VPN: the socket's group.
func TestTheDaemonLogIsReadableOnlyByTheSocketGroup(t *testing.T) {
	group, gid := primaryGroup(t)
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 100, group)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	check := func(when string) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("%s: mode %v, want 0640 (no access for others)", when, got)
		}
		if got := gidOfFile(t, path); got != gid {
			t.Errorf("%s: group %d, want the socket group %d", when, got, gid)
		}
	}
	check("created")
	for i := 0; i < 5; i++ { // forces rotations: every new file must be restricted too
		_, _ = w.Write([]byte("0123456789012345678901234567890123456789012345678901234567890123456789\n"))
	}
	check("after rotation")
	if info, err := os.Stat(path + ".1"); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf("the rotated generation must stay restricted: %v %v", info, err)
	}
}

// An existing 0644 log from an earlier build is narrowed on the next start.
func TestAnExistingWorldReadableLogIsNarrowedWhenAGroupIsConfigured(t *testing.T) {
	group, _ := primaryGroup(t)
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := openBoundedLog(path, 1024, group)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want the old log narrowed to 0640", info.Mode().Perm())
	}
}

// The daemon runs as root and /var/log can be group writable: a symlink planted at the log path
// must not make root append to (and chmod) whatever it points at.
func TestTheDaemonLogRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "daemon.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if w, err := openBoundedLog(link, 1024, ""); err == nil {
		w.Close()
		t.Fatal("opened a symlink as the daemon log")
	}
	data, _ := os.ReadFile(target)
	info, _ := os.Stat(target)
	if string(data) != "precious\n" || info.Mode().Perm() != 0o600 {
		t.Errorf("the symlink target was changed: %q %v", data, info.Mode().Perm())
	}
}

func TestTheDaemonLogRefusesAFifoWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip("cannot make a fifo here")
	}
	done := make(chan error, 1)
	go func() {
		w, err := openBoundedLog(path, 1024, "")
		if err == nil {
			w.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a fifo as the daemon log")
		}
	case <-after5s():
		t.Fatal("opening a fifo blocked the daemon")
	}
}

func after5s() <-chan time.Time { return time.After(5 * time.Second) }

// Under launchd stderr is a file that carries the same lines as the log.
func TestAStderrFileGetsTheSameAccessRuleAsTheLog(t *testing.T) {
	group, gid := primaryGroup(t)
	path := filepath.Join(t.TempDir(), "stderr.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	narrowIfRegular(f, group)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 || gidOfFile(t, path) != gid {
		t.Errorf("mode %v group %d, want 0640 and group %d", info.Mode().Perm(), gidOfFile(t, path), gid)
	}
	// A pipe must be left alone and must not panic.
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	narrowIfRegular(w, group)
}
