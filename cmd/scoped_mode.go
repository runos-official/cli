package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// scopedModeEnvVar is the CI declaration for scoped mode, read when
// --scoped is not passed on the command line (story 346, acceptance
// criterion 6, DECLARED SCOPED MODE; orchestrator ruling on change
// request 26, replacing the earlier D1-D4 which assumed the CLI could
// infer its own credential's verified type).
const scopedModeEnvVar = "RUNOS_SCOPED"

// addScopedModeFlag registers --scoped on every native deploy and
// follow entry point (runos deploy, runos follow, runos jobs follow).
// The CLI never infers scoped-credential behavior from a bearer
// prefix, a storage location, or a request shape (FCR 2638): the
// caller declares it, with this flag or RUNOS_SCOPED=1 for CI.
func addScopedModeFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("scoped", false,
		"declare that this credential is a scoped Account API Key or Personal Access Token: "+
			"use the app-scoped deploy/follow routes only, skip every read outside the "+
			"apps.deploy contract, and treat a refusal from those routes as final (no "+
			"fallback). Also set by RUNOS_SCOPED=1 for CI.")
}

// scopedModeFromFlags resolves whether scoped mode is declared for
// this invocation: the --scoped flag, or RUNOS_SCOPED=1 in the
// environment.
func scopedModeFromFlags(cmd *cobra.Command) bool {
	flag, _ := cmd.Flags().GetBool("scoped")
	return scopedModeDeclared(flag, os.Getenv(scopedModeEnvVar))
}

// scopedModeDeclared is the pure decision scopedModeFromFlags wraps,
// so the rule --scoped OR RUNOS_SCOPED=1 is unit-tested without
// touching the process environment or cobra.
func scopedModeDeclared(flag bool, env string) bool {
	return flag || env == "1"
}
