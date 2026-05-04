package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

var appsCmd = &cobra.Command{
	Use:   "apps",
	Short: "Manage applications",
	Long:  `Manage RunOS applications. Dynamic subcommands (list, show, logs, etc.) come from the manifest; "pull" is a static subcommand that downloads running app config to local YAML.`,
}

func init() {
	appsCmd.AddCommand(appsPullCmd)
	appsCmd.AddCommand(appsDiffCmd)
	appsCmd.AddCommand(appsSyncCmd)
	appsCmd.AddCommand(appsListPreviousUploadsCmd)
}

// appsCmdContext is the request setup shared by every apps subcommand:
// loaded config, an auth token, the resolved cluster id, and a Service
// configured against them.
type appsCmdContext struct {
	cfg   *config.Config
	token string
	cid   string
	svc   *apps.Service
}

// prepareAppsCmd loads config, fetches a fresh ID token, resolves the
// cluster id from --cid (or the configured default), and constructs the
// Service. All apps subcommands need exactly this prelude, extracted to
// keep the cmd files focused on their actual logic.
//
// The cluster id is treated as the user's expected cluster context.
// Subcommands that consume a yaml file are expected to cross-check the
// yaml's cid: against ctx.cid.
func prepareAppsCmd(cmd *cobra.Command) (*appsCmdContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil, fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' or set RUNOS_API_KEY (%w)", err)
	}
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return nil, fmt.Errorf("cluster ID required: pass --cid or set default with 'runos clusters default <cid>'")
	}
	cfg.AccountID = aid
	return &appsCmdContext{
		cfg:   cfg,
		token: token,
		cid:   cid,
		svc:   apps.NewService(cfg.GetAPIURL(), token, cid, aid),
	}, nil
}

// resolveYamlArg returns an absolute yaml path for diff/sync. If args
// has a positional, it's used as-is (after Abs). Otherwise the cwd is
// scanned via apps.FindPulledYAMLs and the unique valid candidate is
// used. Other outcomes surface directly-actionable errors:
//   - 0 candidates, 0 partials → "no runos*.yaml found"
//   - 0 candidates, 1+ partials → "found <files> but missing id/cid/aid"
//   - 2+ candidates → "ambiguous, pick one"
func resolveYamlArg(args []string, cmdName string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get cwd: %w", err)
	}
	scan, err := apps.FindPulledYAMLs(cwd)
	if err != nil {
		return "", fmt.Errorf("scan cwd for yaml: %w", err)
	}
	switch {
	case len(scan.Valid) == 1:
		return scan.Valid[0], nil
	case len(scan.Valid) > 1:
		return "", fmt.Errorf("multiple yaml candidates in current directory: %s, pass one explicitly", strings.Join(relativisePaths(cwd, scan.Valid), ", "))
	case len(scan.Partial) > 0:
		return "", fmt.Errorf(
			"found %s but missing id/cid/aid, looks like a fresh deploy yaml, not a pulled one. Run 'runos apps pull --app-id <id>' to fetch a pulled yaml first, then re-run %q",
			strings.Join(relativisePaths(cwd, scan.Partial), ", "),
			cmdName,
		)
	}
	return "", fmt.Errorf("no runos*.yaml found in current directory. Pass a yaml file as the argument to %q, or cd into a per-app directory", cmdName)
}

// relativisePaths formats absolute paths relative to base for display.
// Falls back to the basename when relative resolution fails.
func relativisePaths(base string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if r, err := filepath.Rel(base, p); err == nil {
			out[i] = r
		} else {
			out[i] = filepath.Base(p)
		}
	}
	return out
}
