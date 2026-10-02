package vpn

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// run executes a command and returns its combined output. Every platform renderer shells out
// through this one helper so a failure message always carries the tool's own words.
func run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
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
	ctx, cancel := context.WithTimeout(context.Background(), serviceQueryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s %w (%s)", name, errQueryTimedOut, serviceQueryTimeout)
	}
	return out, err
}
