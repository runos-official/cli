package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
)

var manifestCmd = &cobra.Command{
	Use:   "manifest",
	Short: "Manage the CLI manifest",
	Long:  `Commands for managing the CLI manifest which defines available commands and API endpoints.`,
}

var manifestUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Force download the latest manifest from the RunOS API",
	Long: `Force download the latest manifest from the RunOS API, bypassing the cache.

This is useful when new API endpoints or commands have been added and you want
to ensure you have the latest definitions.`,
	RunE: runManifestUpdate,
}

func init() {
	manifestCmd.AddCommand(manifestUpdateCmd)
}

func runManifestUpdate(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	configDir := filepath.Join(home, ".runos")

	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)

	fmt.Println("Fetching latest manifest from the RunOS API...")
	m, err := loader.ForceUpdate()
	if err != nil {
		return fmt.Errorf("failed to update manifest: %w", err)
	}

	fmt.Printf("Manifest updated successfully (version %s)\n", m.Version)
	fmt.Printf("Saved to: %s/manifest.json\n", configDir)

	return nil
}
