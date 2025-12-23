package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"cli/internal/config"
	"cli/internal/dynacmd"
	"cli/internal/manifest"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "runos",
	Short: "CLI for interacting with RunOS clusters",
	Long:  `RunOS CLI allows you to manage your RunOS clusters, provision services, and interact with your self-hosted cloud infrastructure.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Static commands - always available
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(mcpCmd)

	// Dynamic commands from manifest
	if err := registerDynamicCommands(); err != nil {
		// Only show warning if it's not a "file not found" error
		fmt.Fprintf(os.Stderr, "Warning: could not load manifest: %v\n", err)
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

	// Load manifest
	loader := manifest.NewLoader(cfg.GetConsoleURL(), configDir)
	m, err := loader.Load()
	if err != nil {
		return err
	}

	// Build and register commands
	executor := dynacmd.NewExecutor(cfg.GetConsoleURL())
	builder := dynacmd.NewBuilder(m, executor)

	for _, cmd := range builder.BuildCommands() {
		rootCmd.AddCommand(cmd)
	}

	return nil
}
