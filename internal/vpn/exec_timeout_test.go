//go:build !windows

package vpn

import (
	"errors"
	"testing"
	"time"
)

/*
An external command the daemon runs cannot hold it forever.

scutil, route, ifconfig, dscacheutil, killall, PowerShell and resolvectl all ran through `run` with
no bound, and the resume runs them while the daemon holds its lock and before it answers anybody. A
hung one stalled the start, the stop and every request behind them.
*/
func TestRunGivesUpOnACommandThatHangs(t *testing.T) {
	previous := commandTimeout
	commandTimeout = 200 * time.Millisecond
	t.Cleanup(func() { commandTimeout = previous })

	started := time.Now()
	_, err := run("sleep", "8")

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("run waited %v for a hung command", elapsed)
	}
	if !errors.Is(err, errQueryTimedOut) {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestRunStillReturnsTheOutputOfAFastCommand(t *testing.T) {
	out, err := run("echo", "ok")
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
