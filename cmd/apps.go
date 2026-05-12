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

// prepareAppsCmd loads config, fetches a fresh ID token, and reads --cid
// or the configured default. The returned context may have an empty cid
// when neither was set: callers that have a yaml positional resolve cid
// from the yaml itself via ctx.bindToYAML; callers that don't have a
// yaml (apps_pull) call ctx.requireCID first.
//
// The Service is built lazily — only once a cid is known — because
// apps.NewService bakes the cid into every URL it constructs.
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
	cfg.AccountID = aid
	ctx := &appsCmdContext{
		cfg:   cfg,
		token: token,
		cid:   cid,
	}
	if cid != "" {
		ctx.svc = apps.NewService(cfg.GetAPIURL(), token, cid, aid)
	}
	return ctx, nil
}

// bindToYAML adopts the cid declared in a loaded yaml. When the user
// also passed --cid (or had a default set), the two must match — this
// is the cross-cluster-push guard. When no cid was set at all, the yaml
// is treated as the source of truth so users can run sync/diff against
// any committed yaml without redundantly naming the cluster.
//
// On success the Service is constructed (or kept) and ctx.cid is final.
func (c *appsCmdContext) bindToYAML(yamlCID string) error {
	switch {
	case c.cid == "":
		c.cid = yamlCID
	case c.cid != yamlCID:
		return fmt.Errorf("cluster mismatch: yaml is for cluster %q but --cid (or default) is %q, refusing to push to a different cluster than expected", yamlCID, c.cid)
	}
	if c.svc == nil {
		c.svc = apps.NewService(c.cfg.GetAPIURL(), c.token, c.cid, c.cfg.AccountID)
	}
	return nil
}

// requireCID errors when no cid was resolved. Used by commands without
// a yaml positional (apps_pull) where there's nothing to source cid
// from on the local side.
func (c *appsCmdContext) requireCID() error {
	if c.cid == "" {
		return fmt.Errorf("cluster ID required: pass --cid or set default with 'runos clusters default <cid>'")
	}
	return nil
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

// autoDetectDeployYAML scans dir for runos*.yaml candidates and returns
// the unique one if exactly one exists (valid or partial — `runos deploy`
// works on both fresh and pulled yamls, so we don't discriminate).
//
// Multi-candidate dirs return a self-documenting error mirroring the
// shape `apps diff` emits via resolveYamlArg, so a user with multiple
// `runos.<cid>.<id>.yaml` files in the same directory sees the list
// instead of the misleading "runos.yaml not found at <path>". The empty
// `("", nil)` result means caller should fall back to its own error
// path (no yaml files at all in dir). Regression target: I15-A.
func autoDetectDeployYAML(cwd string) (string, error) {
	scan, err := apps.FindPulledYAMLs(cwd)
	if err != nil {
		return "", fmt.Errorf("scan cwd for yaml: %w", err)
	}
	all := append([]string{}, scan.Valid...)
	all = append(all, scan.Partial...)
	switch len(all) {
	case 0:
		return "", nil
	case 1:
		return all[0], nil
	default:
		return "", fmt.Errorf(
			"multiple runos yaml candidates in current directory: %s, pass one explicitly with -c <path>",
			strings.Join(relativisePaths(cwd, all), ", "),
		)
	}
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
