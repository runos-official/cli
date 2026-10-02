package vpn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
The daemon's own log file, which exists because two of the three platforms had no log at all.

Measured by reading the service definitions (FCR349): the launchd plist names StandardOutPath, and
the systemd unit and the Windows service name nothing. On Linux the output lands in the journal,
which an ordinary user cannot read; on Windows it is dropped. A file the daemon owns is readable by
the person who needs it, and because the daemon owns it, it can keep it bounded.
*/

func TestTheDaemonLogStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 1024, "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 500; i++ { // 50 KB into a 1 KB cap
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	total := int64(0)
	for _, p := range []string{path, path + ".1"} {
		if info, err := os.Stat(p); err == nil {
			total += info.Size()
		}
	}
	if total > 2*1024+100 {
		t.Fatalf("log holds %d bytes for a 1024 byte cap: nothing bounded it", total)
	}
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Fatal("the live file is empty right after a write: rotation lost the newest lines")
	}
}

func TestAnExistingLogIsAppendedNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("before restart\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := openBoundedLog(path, 1<<20, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("after restart\n"))
	w.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "before restart\nafter restart\n" {
		t.Errorf("a daemon restart must not erase what led up to it, got %q", data)
	}
}

/*
A failed reopen after a rotation must not make the next write rotate again: the second rotation
deletes the <path>.1 the first one just created, so the log loses its previous generation exactly
when the daemon is failing to write.
*/
func TestAFailedReopenDoesNotRotateAgainAndDeleteThePreviousGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := openBoundedLog(path, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	first := strings.Repeat("a", 60) + "\n"
	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}

	realOpen := w.openFn
	w.openFn = func(string) (*os.File, error) { return nil, errors.New("test: no space left") }
	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte(strings.Repeat("b", 60) + "\n")); err == nil {
			t.Fatal("a write with no open file must say so")
		}
	}
	kept, err := os.ReadFile(path + ".1")
	if err != nil || string(kept) != first {
		t.Fatalf("the previous generation was lost while the reopen kept failing: %q, %v", kept, err)
	}

	w.openFn = realOpen
	if _, err := w.Write([]byte("recovered\n")); err != nil {
		t.Fatalf("the log did not recover once the file could be opened again: %v", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "recovered") {
		t.Errorf("the recovered line is not in the live file: %q", data)
	}
	if kept, _ := os.ReadFile(path + ".1"); string(kept) != first {
		t.Errorf("recovery rotated again and lost the previous generation: %q", kept)
	}
}
