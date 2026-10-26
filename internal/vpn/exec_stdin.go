package vpn

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runWithInput runs a command with input on its stdin and returns its combined output. It is `run`
// for programs that read their commands from stdin, which is how scutil takes a script. The same
// commandTimeout applies.
func runWithInput(input, name string, args ...string) ([]byte, error) {
	return runWithInputWithin(commandTimeout, input, name, args...)
}

func runWithInputWithin(timeout time.Duration, input, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	// WaitDelay stops a grandchild that kept the output pipe open from holding the call anyway.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s %w (%s)", name, errQueryTimedOut, timeout)
	}
	return out, err
}
