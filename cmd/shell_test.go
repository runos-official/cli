package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/coder/websocket"

	"github.com/spf13/cobra"
)

// How `runos shell [user] [-- command...]` is taken apart. Both forms have to work, because the
// user argument is positional and `--` is optional, and getting it wrong means either the user
// name is run as a command or the command is read as a user name.
// How `runos shell [-- command...]` is taken apart.
//
// EACH ARGUMENT IS QUOTED, not joined. Joining threw the caller's quoting away and the far end's
// bash re-split what was left, so `grep "error 500" file` silently grepped for "error" across two
// files. Quoting gives the same semantics as `kubectl exec --`: what you typed is what runs, and
// shell syntax needs an explicit shell. The verb takes no positional arguments of its
// own, so a stray word is a MISTAKE rather than something to ignore: dropping it silently would
// open an interactive shell when the caller asked to run something specific.
func TestSplitShellArgs(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantCmd string
		wantErr bool
	}{
		{"no name at all, the caller wants the list", []string{}, "", false},
		{"a command after the dashes", []string{"--", "kubectl", "get", "nodes"}, `'kubectl' 'get' 'nodes'`, false},
		{"a command carrying its own flags", []string{"--", "ls", "-la", "/tmp"}, `'ls' '-la' '/tmp'`, false},
		{"dashes with nothing after them", []string{"--"}, "", false},
		{"a name on its own is fine", []string{"devops"}, "", false},
		{"a name and a command", []string{"devops", "--", "kubectl", "get", "nodes"}, `'kubectl' 'get' 'nodes'`, false},
		{"two bare words is a mistake, not something to ignore", []string{"devops", "extra"}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCmd string
			var gotErr error
			c := &cobra.Command{
				Use:  "shell",
				Args: cobra.ArbitraryArgs,
				RunE: func(cmd *cobra.Command, args []string) error {
					_, gotCmd, gotErr = splitShellArgs(cmd, args)
					return nil
				},
			}
			c.SetArgs(tc.argv)
			c.SetOut(nil)
			c.SetErr(nil)
			if err := c.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if tc.wantErr {
				if gotErr == nil {
					t.Fatalf("expected a refusal for %v", tc.argv)
				}
				// The refusal has to show the working form, or the caller is left guessing.
				if !strings.Contains(gotErr.Error(), "runos shell ") {
					t.Fatalf("the refusal must show the right form, got %q", gotErr)
				}
				return
			}
			if gotErr != nil {
				t.Fatalf("unexpected refusal: %v", gotErr)
			}
			if gotCmd != tc.wantCmd {
				t.Errorf("command: got %q want %q", gotCmd, tc.wantCmd)
			}
		})
	}
}

// Which endings are ordinary and which are faults. Getting this wrong is not cosmetic: an error
// line on every clean logout teaches people to ignore the error line, and then a real one is
// invisible.
func TestIsNormalEnd(t *testing.T) {
	if !isNormalEnd(nil) {
		t.Error("no error at all is a normal end")
	}
	if !isNormalEnd(context.Canceled) {
		t.Error("a cancelled context is the user pressing ctrl-C on the local side")
	}
	// MEASURED 2026-08-21 against a live workspace: typing exit, or a one-shot finishing, closes
	// the socket from Node with NO status code. Before this was handled, every clean session ended
	// with "Error: failed to get reader: received close frame".
	if !isNormalEnd(websocket.CloseError{Code: websocket.StatusNoStatusRcvd}) {
		t.Error("a close frame with no status is still a clean end, and it is the one this far end sends")
	}
	if !isNormalEnd(websocket.CloseError{Code: websocket.StatusNormalClosure}) {
		t.Error("a normal closure is a normal end")
	}
	// A BROKEN PIPE IS: the caller piped this into something that finished reading first.
	if !isNormalEnd(syscall.EPIPE) {
		t.Error("a broken pipe is an ordinary ending, not a fault")
	}
	// AND A BARE EOF IS NOT. This assertion used to say the opposite, which locked the bug in: a
	// TCP connection dropped with no close frame surfaces as "failed to read frame header: EOF",
	// so a broken session exited 0 and looked like a clean logout.
	if isNormalEnd(io.EOF) {
		t.Error("a bare EOF is a dropped connection, not a clean end")
	}

	// And the other direction: a real fault must NOT be swallowed, or a broken session looks like
	// a clean one and the user never learns why their shell vanished.
	if isNormalEnd(errors.New("connection reset by peer")) {
		t.Error("a reset connection is a fault, not a clean end")
	}
	if isNormalEnd(websocket.CloseError{Code: websocket.StatusInternalError}) {
		t.Error("an internal error close is a fault")
	}
}
