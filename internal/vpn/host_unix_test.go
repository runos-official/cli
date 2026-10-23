//go:build !windows

package vpn

import (
	"os"
	"syscall"
	"testing"
	"time"
)

/*
A stop signal that arrives while the daemon is still starting must reach the cleanup.

THE GAP. RunDaemonHost registered for SIGTERM only AFTER start returned, and start runs Resume: an
engine, a Conductor poll (10 s) and a DNS check (5 s). In that window the Go default applied, which
is to exit at once, so nothing removed the split-DNS files the tunnel had just written. launchd
sends SIGTERM for every `vpn restart`, `vpn install` and `vpn uninstall`, so a second restart that
lands during the first one's resume is an ordinary event. After `bootout` nothing restarts the
daemon, and the files stay on the machine.

The test signals its own process, so it also proves the signal is not fatal to the host.
*/
func TestStopSignalDuringStartStillRunsTheStopFunction(t *testing.T) {
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunDaemonHost(func() (func(), error) {
			if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
				t.Errorf("signal self: %v", err)
			}
			// Let the signal be delivered while start is still running.
			time.Sleep(200 * time.Millisecond)
			return func() { close(stopped) }, nil
		})
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop function never ran: a SIGTERM during start was lost")
	}
	if err := <-done; err != nil {
		t.Fatalf("RunDaemonHost: %v", err)
	}
}

/*
A stop signal must still end the process when start is stuck.

THE GAP in the first fix: the handler was registered before start, but start ran on the signalling
goroutine, so nothing read the channel until start returned. Start now covers the instance-lock
wait (up to 15 s) and the resume, and the resume runs external commands (scutil, route, PowerShell,
resolvectl). A hung one swallowed every SIGTERM and SIGINT, and only SIGKILL ended the process.
Before the first fix the Go default would have exited.
*/
func TestStopSignalEndsTheHostWhileStartIsStuck(t *testing.T) {
	previous := shutdownBudget
	shutdownBudget = 300 * time.Millisecond
	t.Cleanup(func() { shutdownBudget = previous })
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	done := make(chan error, 1)
	go func() {
		done <- RunDaemonHost(func() (func(), error) {
			<-block
			return func() {}, nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the host returned nil although start never finished; the exit must say why")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a SIGTERM during a stuck start was swallowed")
	}
}
