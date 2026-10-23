package vpn

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// commandTimeout bounds every external command the daemon and the installer run. Ten seconds is
// far above what route, scutil, resolvectl or a cold PowerShell take, and short enough that a hung
// one cannot hold the daemon's lock past the host's own stop timeout. A variable for tests.
var commandTimeout = 10 * time.Second

// run executes a command and returns its combined output. Every platform renderer shells out
// through this one helper so a failure message always carries the tool's own words. It gives up
// after commandTimeout and reports errQueryTimedOut.
func run(name string, args ...string) ([]byte, error) {
	return runWithin(commandTimeout, name, args...)
}

func runWithin(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// WaitDelay stops a grandchild that kept the output pipe open from holding the call anyway.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s %w (%s)", name, errQueryTimedOut, timeout)
	}
	return out, err
}

// serviceQueryTimeout bounds a question to the OS service manager. A wedged systemd or launchd
// would otherwise hold `vpn diagnose`, which promises it never hangs. A variable for tests.
var serviceQueryTimeout = 5 * time.Second

// errQueryTimedOut marks a bounded command that did not answer in time, as opposed to one that
// answered with a failing exit status.
var errQueryTimedOut = errors.New("did not answer in time")

// runBounded is run with serviceQueryTimeout. The child is killed on timeout; WaitDelay stops a
// grandchild that kept the output pipe open from holding the call anyway.
func runBounded(name string, args ...string) ([]byte, error) {
	return runWithin(serviceQueryTimeout, name, args...)
}
