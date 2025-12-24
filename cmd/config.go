package cmd

import (
	"fmt"

	"cli/internal/config"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
	Long:  `View and modify CLI configuration settings.`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value. Available keys:
  cid          Default cluster ID for commands
  console-url  Console URL for browser authentication
  conductor-url Conductor API URL`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get configuration value(s)",
	Long:  `Get a specific configuration value or all values if no key is provided.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runConfigGet,
}

func init() {
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	switch key {
	case "cid":
		cfg.DefaultClusterID = value
	case "console-url":
		cfg.ConsoleURL = value
	case "conductor-url":
		cfg.ConductorURL = value
	default:
		return fmt.Errorf("unknown config key: %s\nAvailable keys: cid, console-url, conductor-url", key)
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Set %s = %s\n", key, value)
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(args) == 0 {
		// Show all config
		fmt.Printf("account-id:    %s\n", cfg.AccountID)
		fmt.Printf("cid:           %s\n", cfg.DefaultClusterID)
		fmt.Printf("console-url:   %s\n", cfg.GetConsoleURL())
		fmt.Printf("conductor-url: %s\n", cfg.GetConductorURL())
		return nil
	}

	key := args[0]
	switch key {
	case "cid":
		fmt.Println(cfg.DefaultClusterID)
	case "account-id":
		fmt.Println(cfg.AccountID)
	case "console-url":
		fmt.Println(cfg.GetConsoleURL())
	case "conductor-url":
		fmt.Println(cfg.GetConductorURL())
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return nil
}
