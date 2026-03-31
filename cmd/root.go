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

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var rootCmd = &cobra.Command{
	Use:   "runos",
	Short: "CLI for interacting with RunOS clusters",
	Long:  `RunOS CLI allows you to manage your RunOS clusters, provision services, and interact with your self-hosted cloud infrastructure.`,
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
	loader := manifest.NewLoader(cfg.GetConductorURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return err
	}

	// Build and register commands
	// Pass existing commands that have static subcommands so dynamic commands merge with them
	executor := dynacmd.NewExecutor(cfg.GetConductorURL())
	builder := dynacmd.NewBuilder(m, executor).
		WithExistingCommands(clustersCmd, servicesCmd)

	for _, cmd := range builder.BuildCommands() {
		rootCmd.AddCommand(cmd)
	}

	return nil
}
