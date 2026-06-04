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

// servicesHarborCmd is the hand-written parent for Harbor service tools
// (e.g. build-image) whose surface needs a local-filesystem build context
// the manifest can't express. It is wired onto servicesCmd in root.go's
// init() BEFORE the dynamic builder runs (see wireStaticHarborSubtree), so
// the builder's leaf-collision guard sees the static `build-image` and
// skips the manifest-generated, filesystem-blind duplicate, while still
// reusing this parent for the manifest-driven services/harbor/* verbs
// (add, list, show, ...). Wiring it here in services.go's init() instead
// would attach it AFTER BuildCommands (cross-file init order: root.go runs
// before services.go), leaving the manifest leaf to shadow the static one.
var servicesHarborCmd = &cobra.Command{
	Use:   "harbor",
	Short: "Harbor service tools",
	Long:  `Tools for the cluster's Harbor service, such as building and pushing an auxiliary image from a local context.`,
}

// wireStaticHarborSubtree attaches the hand-written harbor subtree to
// servicesCmd. Called from root.go's init() before the dynamic builder so
// the collision guard can see it (see servicesHarborCmd's doc).
func wireStaticHarborSubtree() {
	servicesHarborCmd.AddCommand(servicesHarborBuildImageCmd)
	servicesCmd.AddCommand(servicesHarborCmd)
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
	cfg         *config.Config
	token       string
	cid         string
	cidExplicit bool
	exec        *dynacmd.Executor
	manifest    *manifest.Manifest
}

// prepareServicesCmd loads config, fetches a fresh ID token, reads
// --cid (or the configured default), builds the dynacmd Executor, and
// loads the local manifest cache. Mirrors prepareAppsCmd for the apps
// subcommands.
//
// The returned context may have an empty cid: callers that have a yaml
// positional resolve cid from the yaml itself via ctx.bindToYAML;
// callers that don't (services_pull --type+--service-id) call
// ctx.requireCID. The Executor is cid-independent, so it's always built.
//
// The manifest is loaded from the local cache; users who need a fresh
// shape after a conductor manifest change should run `runos manifest
// update` first.
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
	cidExplicit := cmd.Flags().Changed("cid") && cid != ""
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
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
		cfg:         cfg,
		token:       token,
		cid:         cid,
		cidExplicit: cidExplicit,
		exec:        dynacmd.NewExecutor(cfg.GetAPIURL()),
		manifest:    m,
	}, nil
}

// bindToYAML adopts the cid declared in a loaded service yaml.
// Resolution rules in reconcileCIDWithYAML (shared with appsCmdContext).
// Issue 83.
func (c *servicesCmdContext) bindToYAML(yamlCID string) error {
	resolved, err := reconcileCIDWithYAML(c.cid, c.cidExplicit, yamlCID)
	if err != nil {
		return err
	}
	c.cid = resolved
	return nil
}

// requireCID errors when no cid was resolved. Used by services_pull
// in --type+--service-id mode where there's no yaml on disk yet.
func (c *servicesCmdContext) requireCID() error {
	if c.cid == "" {
		return fmt.Errorf("cluster ID required: pass --cid or set default with 'runos clusters default <cid>'")
	}
	return nil
}
