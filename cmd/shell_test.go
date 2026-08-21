package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/spf13/cobra"
)

// How `runos shell [user] [-- command...]` is taken apart. Both forms have to work, because the
// user argument is positional and `--` is optional, and getting it wrong means either the user
// name is run as a command or the command is read as a user name.
// How `runos shell [-- command...]` is taken apart. The verb takes no positional arguments of its
// own, so a stray word is a MISTAKE rather than something to ignore: dropping it silently would
// open an interactive shell when the caller asked to run something specific.
func TestOneShotCommand(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantCmd string
		wantErr bool
	}{
		{"no arguments, interactive", []string{}, "", false},
		{"a command after the dashes", []string{"--", "kubectl", "get", "nodes"}, "kubectl get nodes", false},
		{"a command carrying its own flags", []string{"--", "ls", "-la", "/tmp"}, "ls -la /tmp", false},
		{"dashes with nothing after them", []string{"--"}, "", false},
		{"a stray word is refused, not ignored", []string{"devops"}, "", true},
		{"a word before the dashes is refused", []string{"devops", "--", "kubectl", "get", "nodes"}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCmd string
			var gotErr error
			c := &cobra.Command{
				Use:  "shell",
				Args: cobra.ArbitraryArgs,
				RunE: func(cmd *cobra.Command, args []string) error {
					gotCmd, gotErr = oneShotCommand(cmd, args)
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
				if !strings.Contains(gotErr.Error(), "runos shell --") {
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
	if !isNormalEnd(io.EOF) {
		t.Error("EOF is a normal end")
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
