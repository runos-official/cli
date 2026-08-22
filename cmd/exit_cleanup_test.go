package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

/*
NEITHER FAILING PATH MAY SKIP ITS OWN CLEANUP.

`os.Exit` runs no deferred functions. Two commands used it to pass a far-end status up, and both
had a defer that mattered sitting a few lines above the call:

  - `runos shell -- <cmd>` puts the terminal into RAW MODE and restores it in a defer. A failing
    one-shot handed the shell back with echo off, so the user's next command was invisible.
  - `runos vms ssh` writes the machine's PRIVATE KEY to a temporary file and deletes it in a defer.
    Every non-zero ssh, which includes an ordinary `exit 1` inside the guest, left the key on disk,
    against this command's own documented promise.

Both now return an error carrying ExitCode(), which Execute() unwraps, so the defers run first and
the caller still sees the status. This test reads the source, because the failure is the ABSENCE of
an unwind and there is nothing to observe at runtime once it is fixed.
*/
func TestFailingPathsReturnRatherThanExit(t *testing.T) {
	for _, tc := range []struct{ file, why string }{
		{"shell.go", "a failing one-shot must restore the terminal from raw mode"},
		{"vms.go", "a failing ssh must delete the VM private key"},
	} {
		b, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if strings.Contains(string(b), "os.Exit(") {
			t.Errorf("%s still calls os.Exit, which runs no defers: %s", tc.file, tc.why)
		}
		if !strings.Contains(string(b), "&exitCodeError{") {
			t.Errorf("%s does not return an exitCodeError, so the far end's status is lost", tc.file)
		}
	}
}

// The contract the fix depends on: Execute unwraps ExitCode() and exits with it.
func TestExecuteHonoursExitCode(t *testing.T) {
	b, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "interface{ ExitCode() int }") || !strings.Contains(s, "os.Exit(ec.ExitCode())") {
		t.Fatal("Execute no longer unwraps ExitCode(), so returning one silently changes the exit status to 1")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
}
