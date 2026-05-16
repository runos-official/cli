package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Regression: `runos services foobar`, `clusters foobar`, `nodes foobar`,
// `jobs foobar`, `account foobar`, `integrations foobar`, `config foobar`
// used to exit 0 with parent help, only `apps` errored. CI gates checking
// $? couldn't distinguish a typo from a valid command. The fix lives in
// strictenParentExitCodes which retrofits the apps.go pattern (Args:NoArgs
// + dummy RunE) onto every silent-help parent in the tree.
func TestStrictenParentExitCodes_UnknownSubcommandErrors(t *testing.T) {
	build := func() *cobra.Command {
		root := &cobra.Command{Use: "root"}
		parent := &cobra.Command{Use: "parent"}
		leaf := &cobra.Command{Use: "leaf", RunE: func(c *cobra.Command, args []string) error { return nil }}
		parent.AddCommand(leaf)
		root.AddCommand(parent)
		return root
	}
	root := build()
	strictenParentExitCodes(root)

	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"parent", "foobar"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected unknown-subcommand error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error %q should mention 'unknown command'", err.Error())
	}

	// Bare `root parent` still falls through to help with no error.
	root = build()
	strictenParentExitCodes(root)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"parent"})
	if err := root.Execute(); err != nil {
		t.Errorf("bare parent should print help and exit 0, got err: %v", err)
	}
}

// Skip parents that already declared RunE: the override would clobber
// intentional behaviour (e.g. clustersDefaultCmd uses MaximumNArgs(1)).
func TestStrictenParentExitCodes_PreservesExistingRunE(t *testing.T) {
	called := false
	leaf := &cobra.Command{
		Use:  "leaf",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			called = true
			return nil
		},
	}
	// Give leaf a child so HasSubCommands() is true but RunE is still set.
	leaf.AddCommand(&cobra.Command{Use: "grandchild", RunE: func(c *cobra.Command, args []string) error { return nil }})

	strictenParentExitCodes(leaf)
	leaf.SetOut(io.Discard)
	leaf.SetErr(io.Discard)
	leaf.SetArgs([]string{"only-arg"})
	if err := leaf.Execute(); err != nil {
		t.Fatalf("expected leaf's own RunE to run, got err: %v", err)
	}
	if !called {
		t.Errorf("leaf's RunE was clobbered by strictenParentExitCodes")
	}
}

// Recursion: nested parents (parent/sub) both need the pattern. Verifies
// `root parent sub foobar` errors, not just `root parent foobar`.
func TestStrictenParentExitCodes_RecursesIntoNestedParents(t *testing.T) {
	root := &cobra.Command{Use: "root"}
	parent := &cobra.Command{Use: "parent"}
	sub := &cobra.Command{Use: "sub"}
	leaf := &cobra.Command{Use: "leaf", RunE: func(c *cobra.Command, args []string) error { return nil }}
	sub.AddCommand(leaf)
	parent.AddCommand(sub)
	root.AddCommand(parent)

	strictenParentExitCodes(root)

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"parent", "sub", "foobar"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected unknown-subcommand error on nested parent, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("nested-parent error %q should mention 'unknown command'", err.Error())
	}
}
