package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// pullCmd is a top-level alias for `runos apps pull`. The round-trip sibling
// of `runos deploy` is `runos apps pull`, but users (and LLM agents) reach
// for `runos pull` by analogy with `runos deploy` and previously got a bare
// "unknown command" error with no suggestion. Hoisting `pull` as a top-level
// alias mirrors the `deploy` shape and makes the round-trip symmetric.
//
// The alias reuses appsPullCmd's flag pointers via pflag.AddFlag so the two
// command surfaces stay in sync automatically: any flag added to apps pull
// shows up on the alias without a second declaration.
var pullCmd = &cobra.Command{
	Use:          appsPullCmd.Use,
	Short:        appsPullCmd.Short + " (alias for `runos apps pull`)",
	Long:         appsPullCmd.Long,
	Args:         appsPullCmd.Args,
	SilenceUsage: true,
	RunE:         runAppsPull,
}

func init() {
	// Share the flag definitions with appsPullCmd. The pflag.Flag pointers
	// carry the same backing Value storage, so parsing on pullCmd writes
	// into the same memory that runAppsPull reads via cmd.Flag(...).
	//
	// Source-file order puts apps_pull.go's init() before pull.go's, so
	// appsPullCmd.Flags() is fully populated by the time this runs.
	appsPullCmd.Flags().VisitAll(func(f *pflag.Flag) {
		pullCmd.Flags().AddFlag(f)
	})
	// Carry over the `--id` -> `--app-id` normalization so the alias accepts
	// the same flag spelling that dynacmd-generated apps_* commands use.
	pullCmd.Flags().SetNormalizeFunc(appsPullCmd.Flags().GetNormalizeFunc())
}
