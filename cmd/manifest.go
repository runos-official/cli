package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
)

// filterManifestCommandPaths returns the command-path strings from cmds
// whose path contains filter as a case-insensitive substring. An empty
// filter is treated as "match everything". The return slice is always
// non-nil so the --json caller emits `[]` (not `null`) when nothing
// matches: jq's `.[]` and `.length` error on null but work on [],
// which silently broke CI/LLM consumers piping the introspection
// output.
func filterManifestCommandPaths(cmds []manifest.Command, filter string) []string {
	needle := strings.ToLower(filter)
	out := []string{}
	for _, c := range cmds {
		if needle != "" && !strings.Contains(strings.ToLower(c.Command), needle) {
			continue
		}
		out = append(out, c.Command)
	}
	return out
}

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

var manifestShowCmd = &cobra.Command{
	Use:   "show [command-path]",
	Short: "Show the manifest version, command count, or a single command's full definition",
	Long: `Inspect the local CLI manifest.

With no argument, prints version + command count + a one-line-per-command list.
With a command path (e.g. "apps/logs", "account/api-keys/add"), prints the full JSON definition for that command — endpoint, method, input fields, output fields, mcp tags.

This is the LLM/MCP-friendly introspection surface for "what commands and endpoints exist". Updates the local cache on miss, so the data reflects the latest published manifest.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runManifestShow,
}

var manifestListCmd = &cobra.Command{
	Use:   "list [substring]",
	Short: "List manifest command paths (optionally filtered by substring)",
	Long: `Print one command path per line. Optional substring filter matches anywhere in the path (case-insensitive).

Examples:
  runos manifest list                  # every command
  runos manifest list services         # only services/*
  runos manifest list secret           # apps/secret-files/*, apps/secret-env-vars/*, ...`,
	Args: cobra.MaximumNArgs(1),
	RunE: runManifestList,
}

var manifestShowJSON bool
var manifestListJSON bool

func init() {
	manifestCmd.AddCommand(manifestUpdateCmd)
	manifestCmd.AddCommand(manifestShowCmd)
	manifestCmd.AddCommand(manifestListCmd)

	manifestShowCmd.Flags().BoolVarP(&manifestShowJSON, "json", "j", false, "Output as JSON")
	manifestListCmd.Flags().BoolVarP(&manifestListJSON, "json", "j", false, "Output as JSON")
}

func loadManifest() (*manifest.Manifest, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	loader := manifest.NewLoader(cfg.GetAPIURL(), filepath.Join(home, ".runos"))
	return loader.Load()
}

func runManifestShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	m, err := loadManifest()
	if err != nil {
		return err
	}

	if len(args) == 1 {
		target := args[0]
		// Resolve CLI-side aliases (e.g. `account/mcp/show` →
		// `clusters/mcp/show`) so the alias spellings users invoke as
		// runnable commands also surface in `manifest show` queries.
		// Pre-fix `manifest show account/mcp/show` errored "no command
		// in manifest" even though `runos account mcp show` works.
		canonical := dynacmd.ResolveAliasToCanonical(target)
		for _, c := range m.Commands {
			if c.Command == target || c.Command == canonical {
				out, err := json.MarshalIndent(c, "", "  ")
				if err != nil {
					return err
				}
				fmt.Println(string(out))
				return nil
			}
		}
		return fmt.Errorf("no command %q in manifest version %s (run 'runos manifest list %s' to find similar paths)", target, m.Version, target)
	}

	if manifestShowJSON {
		summary := map[string]any{
			"version": m.Version,
			"count":   len(m.Commands),
		}
		out, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Manifest version: %s\n", m.Version)
	fmt.Printf("Commands:         %d\n", len(m.Commands))
	fmt.Println()
	fmt.Println("Run 'runos manifest list' to enumerate paths, or 'runos manifest show <path>' for one command's full definition.")
	return nil
}

func runManifestList(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	m, err := loadManifest()
	if err != nil {
		return err
	}

	filter := ""
	if len(args) == 1 {
		filter = args[0]
	}
	paths := filterManifestCommandPaths(m.Commands, filter)

	if manifestListJSON {
		out, err := json.MarshalIndent(paths, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}

	for _, p := range paths {
		fmt.Println(p)
	}
	return nil
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
