package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/auth"
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
With a cluster ID argument, sets it as the new default after validating
that it has a legal shape and exists on the account.

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
	cmd.SilenceUsage = true

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if len(args) == 0 {
		if cfg.DefaultClusterID == "" {
			fmt.Println("No default cluster set")
			fmt.Println("Set one with: runos clusters default <cid>")
			return nil
		}
		fmt.Println(cfg.DefaultClusterID)
		return nil
	}

	cid, err := normalizeClusterID(args[0])
	if err != nil {
		return err
	}

	ids, fetchErr := fetchClusterIDs(cfg)
	if fetchErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not verify cluster %q exists on the account (%v); setting anyway. Run 'runos clusters list' once authenticated to confirm.\n", cid, fetchErr)
	} else if !slices.Contains(ids, cid) {
		return fmt.Errorf("cluster %q not found on this account; run 'runos clusters list' to see available cluster IDs", cid)
	}

	cfg.DefaultClusterID = cid
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("Default cluster set to: %s\n", cid)
	return nil
}

// normalizeClusterID trims surrounding whitespace from the user-supplied
// cluster id and validates that what remains is a legal identifier (the
// conductor's [A-Za-z0-9_-] alphabet). The trim catches the common
// shell-paste case where extra spaces sneak into the argument and would
// otherwise be stored verbatim and break every subsequent command. The
// shape check rejects path-traversal and other obviously malformed
// strings before they reach the on-disk config.
func normalizeClusterID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("cluster id is empty")
	}
	if err := apps.ValidateIdentifier("cluster id", trimmed); err != nil {
		return "", err
	}
	return trimmed, nil
}

// fetchClusterIDs returns the list of cluster ids the account currently
// owns. Used by `clusters default` to refuse setting a default cid that
// doesn't exist. A network or auth failure returns the underlying error
// so the caller can downgrade to a warn-and-save (early-setup flows
// where the user is wiring up their config without a live session
// shouldn't be hard-blocked, but a live session should reject typos).
func fetchClusterIDs(cfg *config.Config) ([]string, error) {
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil, fmt.Errorf("no account id configured")
	}
	baseURL := cfg.GetAPIURL()
	if baseURL == "" {
		return nil, fmt.Errorf("no API URL configured")
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("not authenticated: %w", err)
	}

	reqURL := fmt.Sprintf("%s/%s/clusters", strings.TrimRight(baseURL, "/"), url.PathEscape(aid))
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d)", resp.StatusCode)
	}

	return extractClusterIDs(body)
}

// extractClusterIDs parses a `/:aid/clusters` response and returns the
// `id` field of each entry. Conductor's list-style endpoints are
// migrating to single-key envelopes (`{clusters: [...]}`) while older
// builds still return a bare array, so the helper accepts both shapes.
// Extracted as a pure function so the regression test can exercise the
// envelope / bare-array / malformed cases without spinning up a server.
func extractClusterIDs(body []byte) ([]string, error) {
	trimmed := []byte(strings.TrimSpace(string(body)))
	var items []map[string]any
	if err := json.Unmarshal(trimmed, &items); err != nil {
		var envelope map[string]json.RawMessage
		if eErr := json.Unmarshal(trimmed, &envelope); eErr != nil {
			return nil, fmt.Errorf("failed to parse clusters list: %w", err)
		}
		var inner json.RawMessage
		for _, v := range envelope {
			vt := []byte(strings.TrimSpace(string(v)))
			if len(vt) > 0 && vt[0] == '[' {
				inner = v
				break
			}
		}
		if len(inner) == 0 {
			return nil, fmt.Errorf("clusters list envelope has no array field")
		}
		if iErr := json.Unmarshal(inner, &items); iErr != nil {
			return nil, fmt.Errorf("failed to parse clusters list envelope: %w", iErr)
		}
	}

	ids := make([]string, 0, len(items))
	for _, it := range items {
		// Conductor's clusters list emits each row's id under `cid`; fall
		// back to `id` for forward compatibility with any future shape
		// that follows the wider list-endpoint convention.
		if id, ok := it["cid"].(string); ok && id != "" {
			ids = append(ids, id)
			continue
		}
		if id, ok := it["id"].(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
