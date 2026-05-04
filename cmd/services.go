package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"

	"github.com/spf13/cobra"
)

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Manage services",
	Long:  `Manage RunOS services. Use subcommands to list, show, add, or delete services.`,
}

func init() {
	servicesCmd.AddCommand(servicesPullCmd)
	servicesCmd.AddCommand(servicesDiffCmd)
	servicesCmd.AddCommand(servicesSyncCmd)
}

// servicesCmdContext is the resolved setup for every static services
// subcommand: loaded config, auth token, dynacmd Executor, manifest, and
// the resolved cluster id. The Executor and Manifest let the static
// pull/diff/sync commands talk to the conductor through the same
// manifest-driven path the dynamic commands use, so adding a new
// service type to the manifest lights up automatically here.
type servicesCmdContext struct {
	cfg      *config.Config
	token    string
	cid      string
	exec     *dynacmd.Executor
	manifest *manifest.Manifest
}

// prepareServicesCmd loads config, fetches a fresh ID token, resolves
// the cluster id, builds the dynacmd Executor, and loads the local
// manifest cache. Mirrors prepareAppsCmd for the apps subcommands.
//
// The manifest is loaded from the local cache; users who need a fresh
// shape after a conductor manifest change should run `runos manifest
// update` first. Same convention as every other manifest-driven path.
func prepareServicesCmd(cmd *cobra.Command) (*servicesCmdContext, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil, fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	cfg.AccountID = aid
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
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home dir: %w", err)
	}
	configDir := filepath.Join(home, ".runos")
	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	// Load() falls through to a remote fetch when the cache is missing
	// or stale; LoadLocal() is cache-only. Headless CI runs (PAT, no
	// pre-existing ~/.runos) need the remote fetch on first call.
	m, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load manifest (run 'runos manifest update'?): %w", err)
	}
	return &servicesCmdContext{
		cfg:      cfg,
		token:    token,
		cid:      cid,
		exec:     dynacmd.NewExecutor(cfg.GetAPIURL()),
		manifest: m,
	}, nil
}
