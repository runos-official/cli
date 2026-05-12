package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"

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

var configGetJSON bool

func init() {
	configCmd.AddCommand(configEnvCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)

	configGetCmd.Flags().BoolVarP(&configGetJSON, "json", "j", false, "Output as JSON")
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

// configKeyAliases maps file-form (snake_case, as it appears in
// ~/.runos/config.json) and other historical spellings to the canonical
// CLI key. Users who read the file and then try `config get` /
// `config set` with the file key shouldn't hit "unknown config key";
// I8-I closes that footgun.
var configKeyAliases = map[string]string{
	"default_cluster_id": "cid",
	"account_id":         "account-id",
	"console_url":        "console-url",
	"conductor_url":      "api-url",
	"api_url":            "api-url",
}

// normalizeConfigKey applies configKeyAliases and returns the canonical
// CLI key. Unknown keys are returned unchanged so the caller can emit
// the "unknown config key" error with the original spelling for clarity.
func normalizeConfigKey(key string) string {
	if alias, ok := configKeyAliases[key]; ok {
		return alias
	}
	return key
}

// cidCharset bounds the shape of a cluster id stored as the CLI default.
// Mirrors conductor's normalizeCid regex (lowercase alphanumeric, 3-16
// chars); the cluster id charset is server-determined so this stays in
// sync if conductor ever widens or narrows the rule.
var cidCharset = regexp.MustCompile(`^[a-z0-9]{3,16}$`)

// validateConfigSet enforces per-key value constraints for
// `runos config set`. Pre-fix the setter accepted any string for any
// key, including the empty string for cid, which silently broke every
// downstream command that fell back to the default cluster. Validation
// runs before the write so a typo can't poison the config file.
// Regression target: I18-J.
func validateConfigSet(key, value string) error {
	switch key {
	case "cid":
		if value == "" {
			return fmt.Errorf("cid must not be empty; use 'runos clusters list' to find a valid cluster id")
		}
		if !cidCharset.MatchString(value) {
			return fmt.Errorf("cid %q is not a valid cluster id shape (expected lowercase alphanumeric, 3-16 chars); use 'runos clusters list' to find a valid cluster id", value)
		}
	case "console-url", "api-url":
		if value == "" {
			return fmt.Errorf("%s must not be empty", key)
		}
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s must be a valid URL with scheme and host (got %q)", key, value)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%s scheme must be http or https (got %q)", key, u.Scheme)
		}
	}
	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	key := normalizeConfigKey(args[0])
	value := args[1]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	switch key {
	case "cid":
		if err := validateConfigSet(key, value); err != nil {
			return err
		}
		cfg.DefaultClusterID = value
	case "console-url":
		if err := validateConfigSet(key, value); err != nil {
			return err
		}
		cfg.ConsoleURL = value
	case "api-url":
		if err := validateConfigSet(key, value); err != nil {
			return err
		}
		cfg.ConductorURL = value
	default:
		return fmt.Errorf("unknown config key: %s\nValid keys: cid, console-url, api-url", args[0])
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Set %s = %s\n", key, value)
	if key == "cid" {
		// The value isn't verified against the server here: validating
		// would require a network roundtrip and reuse of the auth path,
		// which is heavier than the CLI-config surface should carry.
		// Surface the verification step instead so a typo is recoverable
		// (I8-J).
		fmt.Println("Note: cluster id not verified against the server. Run 'runos clusters list' to confirm it exists in your account.")
	}
	return nil
}

func runConfigGet(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	all := map[string]string{
		"env":         cfg.Env,
		"account-id":  cfg.AccountID,
		"cid":         cfg.DefaultClusterID,
		"console-url": cfg.GetConsoleURL(),
		"api-url":     cfg.GetAPIURL(),
	}

	if len(args) == 0 {
		if configGetJSON {
			return emitConfigJSON(cmd, all)
		}
		fmt.Printf("env:         %s\n", all["env"])
		fmt.Printf("account-id:  %s\n", all["account-id"])
		fmt.Printf("cid:         %s\n", all["cid"])
		fmt.Printf("console-url: %s\n", all["console-url"])
		fmt.Printf("api-url:     %s\n", all["api-url"])
		return nil
	}

	key := normalizeConfigKey(args[0])
	value, ok := all[key]
	if !ok {
		return fmt.Errorf("unknown config key: %s\nValid keys: env, account-id, cid, console-url, api-url", args[0])
	}
	if configGetJSON {
		return emitConfigJSON(cmd, map[string]string{key: value})
	}
	fmt.Println(value)
	return nil
}

func emitConfigJSON(cmd *cobra.Command, payload map[string]string) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}
