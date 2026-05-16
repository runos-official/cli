package cmd

import "github.com/spf13/cobra"

// strictenParentExitCodes walks the command tree and converts every
// silent-help parent (has subcommands, no Run/RunE) into one that
// rejects unknown args with a non-zero exit, so typos like
// `runos clusters lst` fail CI gates instead of printing parent help
// and exiting 0.
//
// Cobra's default legacyArgs validator only fires the "unknown command"
// error when !HasParent() — the root errors, every intermediate parent
// is silently permissive. The fix is the same pattern apps.go uses:
// Args:NoArgs plus a dummy RunE that prints help (cobra short-circuits
// non-runnable commands to help BEFORE ValidateArgs, so the parent
// needs a RunE for the Args check to fire at all).
//
// Idempotent: parents that already declared RunE/Run (e.g. apps in
// apps.go) are skipped. Children are walked first so explicit
// definitions on leaves never get clobbered.
func strictenParentExitCodes(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		strictenParentExitCodes(child)
	}
	if !cmd.HasSubCommands() {
		return
	}
	if cmd.Run != nil || cmd.RunE != nil {
		return
	}
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return c.Help()
	}
}
