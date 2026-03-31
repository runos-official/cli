package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

var clustersCmd = &cobra.Command{
	Use:   "clusters",
	Short: "Manage clusters",
	Long:  `Manage RunOS clusters. Use subcommands to list, show, add, or delete clusters.`,
}

var clustersDefaultCmd = &cobra.Command{
	Use:   "default [cid]",
	Short: "Get or set the default cluster",
	Long: `Get or set the default cluster ID used for commands.

Without arguments, shows the current default cluster.
With a cluster ID argument, sets it as the new default.

Examples:
  runos clusters default         # Show current default
  runos clusters default mycluster2     # Set mycluster2 as default`,
	Args: cobra.MaximumNArgs(1),
	RunE: runClustersDefault,
}

func init() {
	clustersCmd.AddCommand(clustersDefaultCmd)
}

func runClustersDefault(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(args) == 0 {
		// Show current default
		if cfg.DefaultClusterID == "" {
			fmt.Println("No default cluster set")
			fmt.Println("Set one with: runos clusters default <cid>")
			return nil
		}
		fmt.Println(cfg.DefaultClusterID)
		return nil
	}

	// Set new default
	cfg.DefaultClusterID = args[0]
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Default cluster set to: %s\n", args[0])
	return nil
}
