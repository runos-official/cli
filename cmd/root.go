// Package cmd implements CLI commands for the RunOS CLI.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/update"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// manifestFileName is the on-disk filename for the cached CLI manifest
// inside ~/.runos/. Mirrors the constant used inside internal/manifest;
// duplicated here so root.go can probe its existence without exporting
// an extra helper from the package.
const manifestFileName = "manifest.json"

// shouldBootstrapManifest reports whether PersistentPreRunE should
// attempt to fetch the manifest on this command invocation. Returns
// true when the local cache is missing AND the command is one that
// might use the manifest (i.e. not version/help/config/update/manifest
// itself). Pure function so the skip-list contract is testable.
//
// V6 fix: pre-fix, no first-run bootstrap existed, so manifest-driven
// commands like `runos services sync` hard-failed on a fresh CI install
// while `runos deploy` (statically defined) only soft-warned. Bootstrap
// here unifies both paths.
func shouldBootstrapManifest(cmdName, parentName string, manifestPresent bool) bool {
	if manifestPresent {
		return false
	}
	switch cmdName {
	case "config", "env", "version", "help", "update":
		return false
	}
	// `runos manifest update` does its own fetch. Skip the bootstrap to
	// avoid double-fetching.
	if parentName == "manifest" {
		return false
	}
	return true
}

var rootCmd = &cobra.Command{
	Use:     "runos",
	Short:   "CLI for interacting with RunOS clusters",
	Long:    `RunOS CLI allows you to manage your RunOS clusters, provision services, and interact with your self-hosted cloud infrastructure.`,
	Version: version.Version,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config check for these commands
		cmdName := cmd.Name()
		if cmdName == "config" || cmdName == "env" || cmdName == "version" || cmdName == "help" || cmdName == "update" {
			return nil
		}
		// Also skip for parent commands that have their own subcommands
		if cmd.Parent() != nil && cmd.Parent().Name() == "config" {
			return nil
		}

		if !config.Exists() {
			if _, err := config.InitFromRemote(); err != nil {
				return fmt.Errorf("failed to initialize config: %w\nRun 'runos config env <environment>' to set up manually", err)
			}
		}

		// V6: bootstrap the manifest on first run when the cache file is
		// missing, parallel to the config bootstrap above. Soft-warn on
		// failure so a transient network blip doesn't block the run; the
		// dependent command (apps_pull, services_*) will surface its own
		// "(run 'runos manifest update'?)" hint with the wrapped error.
		if home, err := os.UserHomeDir(); err == nil {
			configDir := filepath.Join(home, ".runos")
			manifestPath := filepath.Join(configDir, manifestFileName)
			_, statErr := os.Stat(manifestPath)
			parentName := ""
			if cmd.Parent() != nil {
				parentName = cmd.Parent().Name()
			}
			if shouldBootstrapManifest(cmd.Name(), parentName, statErr == nil) {
				cfg, cfgErr := config.Load()
				if cfgErr == nil {
					loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
					if _, err := loader.Load(); err != nil && term.IsTerminal(int(os.Stderr.Fd())) {
						fmt.Fprintf(os.Stderr, "Note: failed to fetch CLI manifest on first run (%v). Run 'runos manifest update' to retry.\n", err)
					}
				}
			}
		}

		// Check for CLI updates (cached, runs at most once per hour)
		// Only show update notice when stderr is a terminal (not in scripts/CI)
		if term.IsTerminal(int(os.Stderr.Fd())) {
			if latestVersion := update.CheckForUpdate(); latestVersion != "" {
				fmt.Fprintf(os.Stderr, "\nUpdate available: %s (current: %s)\n", latestVersion, update.CurrentVersion())
				fmt.Fprintln(os.Stderr, "Run 'runos update' to install the latest version.")
			}
		}

		return nil
	},
}

// Execute runs the root command and exits with code 1 on error.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// I24-Q: align cobra's auto-generated `runos --version` / `runos -v`
	// output with the bare format the explicit `runos version` subcommand
	// emits. Pre-fix cobra rendered "runos version <X>" while the
	// subcommand printed just "<X>"; CI gates piping through one shape
	// stripped a prefix that the other shape didn't have. Both forms now
	// emit the bare version string + trailing newline.
	rootCmd.SetVersionTemplate("{{.Version}}\n")

	// Static commands - always available
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(mcpCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(followCmd)
	rootCmd.AddCommand(deployCmd)
	rootCmd.AddCommand(manifestCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(updateCmd)

	// Static commands that will be merged with dynamic commands from manifest
	// These commands have static subcommands (e.g., "clusters default") that coexist
	// with dynamic subcommands from the manifest (e.g., "clusters list", "clusters show")
	rootCmd.AddCommand(clustersCmd)
	rootCmd.AddCommand(servicesCmd)
	rootCmd.AddCommand(appsCmd)

	// Dynamic commands from manifest
	if err := registerDynamicCommands(); err != nil {
		fmt.Fprintf(os.Stderr, "Unable to load manifest: %v\n", err)
	}
}

func registerDynamicCommands() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Get config directory for manifest storage
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(home, ".runos")

	// Load manifest from Conductor API
	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return err
	}

	// Build and register commands
	// Pass existing commands that have static subcommands so dynamic commands merge with them.
	// Static `config` and `deploy` commands also need to be registered as existing so the
	// dynamic builder merges any manifest-side definitions instead of producing duplicate
	// top-level entries (which used to render `config` and `deploy` twice in `runos --help`).
	executor := dynacmd.NewExecutor(cfg.GetAPIURL())
	builder := dynacmd.NewBuilder(m, executor).
		WithExistingCommands(clustersCmd, servicesCmd, appsCmd, configCmd, deployCmd, mcpCmd)

	for _, cmd := range builder.BuildCommands() {
		rootCmd.AddCommand(cmd)
	}

	return nil
}
