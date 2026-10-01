package vpn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
The CLI half of the connect path. Sign-in, device registration and the session mint all run in the
person's own process, not in the daemon, so the daemon log cannot know they happened. The CLI writes
its own short record of the last attempt, owned by the user and replaced on each `up`.
*/

func TestTraceRecordsTheLastAttemptOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-up.log")

	first := StartTrace(path, "1.2.3")
	first.Fail("enrol", errors.New("HTTP 500"))
	second := StartTrace(path, "1.2.3")
	second.OK("credential", "kind=interactive")
	second.OK("enrol", "device=7b1e44d0")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "HTTP 500") {
		t.Errorf("the previous attempt leaked into this one, so a fixed problem would still read as current:\n%s", data)
	}
	if !strings.Contains(string(data), "step=enrol status=ok") {
		t.Errorf("the current attempt is missing:\n%s", data)
	}
	if info, _ := os.Stat(path); info != nil && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode %v: the trace belongs to the person who ran it", info.Mode().Perm())
	}
}

func TestTraceRedactsWhatItWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-up.log")
	tr := StartTrace(path, "1.2.3")
	tr.Fail("session", errors.New("POST https://api.example.com/x?token=abc123 failed: Bearer zzz.yyy.xxx"))
	data, _ := os.ReadFile(path)
	for _, leak := range []string{"abc123", "zzz.yyy.xxx"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("%q reached the file:\n%s", leak, data)
		}
	}
}

func TestANilTraceIsSilent(t *testing.T) {
	var tr *Trace
	tr.OK("credential", "x") // must not panic: commands run without a writable home
	tr.Fail("enrol", errors.New("x"))
}

func TestReadTraceGivesTheLinesBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-up.log")
	tr := StartTrace(path, "1.2.3")
	tr.OK("credential", "kind=interactive")
	tr.Fail("enrol", errors.New("HTTP 500"))
	lines := ReadTrace(path)
	failure := LastUnresolvedFailure(lines)
	if failure == nil || failure.Step != "enrol" {
		t.Fatalf("got %+v from %v", failure, lines)
	}
	if ReadTrace(filepath.Join(t.TempDir(), "absent")) != nil {
		t.Error("a missing trace is no lines, not an error")
	}
}
