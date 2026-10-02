package vpn

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

/*
A crash must not be lost, and a file nobody rotates must not grow for ever.

launchd used to be handed /var/log/runos-vpn.log for stdout and stderr. The daemon cannot rotate that
file, so every line the daemon wrote also went into a file with no cap. The daemon now writes its own
bounded log, so launchd's streams go nowhere, and the one thing stderr alone carried (the report of a
Go panic) goes to a small file of its own, trimmed when the daemon starts.
*/

func TestSetupDaemonLogKeepsTheCrashFileSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	crash := path + ".crash"
	if err := os.WriteFile(crash, []byte(strings.Repeat("old panic line\n", 20000)), 0o640); err != nil { // ~300 KB
		t.Fatal(err)
	}
	prev := log.Writer()
	t.Cleanup(func() {
		log.SetOutput(prev)
		debug.SetCrashOutput(nil, debug.CrashOptions{})
	})

	closeLog := SetupDaemonLog(path, "", false)
	defer closeLog()

	info, err := os.Stat(crash)
	if err != nil {
		t.Fatalf("the crash file was not created: %v", err)
	}
	if info.Size() > maxCrashLogBytes {
		t.Errorf("the crash file is %d bytes, over its %d cap", info.Size(), maxCrashLogBytes)
	}
}

func TestSetupDaemonLogCreatesTheCrashFileWhenThereIsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	prev := log.Writer()
	t.Cleanup(func() {
		log.SetOutput(prev)
		debug.SetCrashOutput(nil, debug.CrashOptions{})
	})
	closeLog := SetupDaemonLog(path, "", false)
	defer closeLog()
	if _, err := os.Stat(path + ".crash"); err != nil {
		t.Fatalf("the crash file was not created: %v", err)
	}
}
