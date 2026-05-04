package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage CLI configuration",
	Long:  `View and modify CLI configuration settings.`,
}

var configEnvCmd = &cobra.Command{
	Use:   "env <environment>",
	Short: "Set the target environment",
	Long: `Set the target environment for the CLI.

This fetches the environment configuration from the RunOS CDN and applies
the specified environment preset. You will need to login again after switching.

To use custom URLs (e.g. for local development), use:
  runos config set conductor-url http://localhost:3025
  runos config set console-url http://localhost:5177

Examples:
  runos config env beta
  runos config env dev`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigEnv,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value. Available keys:
  cid            Default cluster ID for commands
  console-url    Console URL for browser authentication
  conductor-url  Conductor API URL`,
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
	configCmd.AddCommand(configEnvCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
}

func runConfigEnv(cmd *cobra.Command, args []string) error {
	envName := args[0]

	fmt.Printf("Fetching configuration for %s...\n", envName)

	cfg, err := config.InitFromRemoteEnv(envName)
	if err != nil {
		return err
	}

	fmt.Printf("Configured %s environment\n", envName)
	fmt.Printf("  Console:   %s\n", cfg.ConsoleURL)
	fmt.Printf("  API:       %s\n", cfg.ConductorURL)
	fmt.Printf("\nRun 'runos login' to authenticate.\n")
	return nil
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
	case "api-url":
		cfg.ConductorURL = value
	default:
		return fmt.Errorf("unknown config key: %s\nAvailable keys: cid, console-url, api-url", key)
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
		fmt.Printf("env:         %s\n", cfg.Env)
		fmt.Printf("account-id:  %s\n", cfg.AccountID)
		fmt.Printf("cid:         %s\n", cfg.DefaultClusterID)
		fmt.Printf("console-url: %s\n", cfg.GetConsoleURL())
		fmt.Printf("api-url:     %s\n", cfg.GetAPIURL())
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
	case "api-url":
		fmt.Println(cfg.GetAPIURL())
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return nil
}
