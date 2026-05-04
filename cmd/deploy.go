package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/jobs"
	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy an app from the current directory",
	Long: `Deploy an application to a RunOS cluster.

This command reads a runos.yaml configuration file from the current directory,
creates a tarball of the project files, and deploys it to the specified cluster.

The runos.yaml file should contain at minimum:
  app: "My App Name"
  port: 8080

Optional fields include dockerfile, resource limits, and service dependencies.`,
	RunE: runDeploy,
}

var deploySyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync local config with deployed app state",
	Long: `Sync the local runos.yaml config file with the deployed application state.

This command fetches the app ID and dependency IDs from the deployed application
and updates the local config file. Use this to:
- Link an existing config to a deployed app
- Refresh IDs after deployment
- Restore IDs that were accidentally removed`,
	RunE: runDeploySync,
}

func init() {
	deployCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file")
	deployCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	deployCmd.Flags().BoolP("follow", "f", false, "follow job progress until completion")
	deployCmd.Flags().BoolP("json", "j", false, "output response as JSON")
	deployCmd.Flags().Bool("force", false, "deploy even when local diverges from the server (skips the pre-deploy drift gate)")

	// Add sync subcommand
	deploySyncCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file")
	deploySyncCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	deployCmd.AddCommand(deploySyncCmd)
}

func runDeploy(cmd *cobra.Command, args []string) error {
	// Load CLI config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get auth token (RUNOS_API_KEY env var, falling back to Firebase ID token).
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' or set RUNOS_API_KEY (%w)", err)
	}

	// Get cluster ID
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
	}

	// Get account ID (env var takes precedence so CI runs without a config file).
	aid := cfg.GetAccountID()
	if aid == "" {
		return fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	cfg.AccountID = aid

	// Load deploy config
	configPath, _ := cmd.Flags().GetString("config")
	if !filepath.IsAbs(configPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		configPath = filepath.Join(cwd, configPath)
	}

	deployConfig, err := deploy.LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Validate AID
	if err := deploy.ValidateAID(deployConfig.AID, cfg.AccountID); err != nil {
		return err
	}

	// Create deploy service
	configDir := filepath.Dir(configPath)
	svc := deploy.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)

	// Check if app already exists but config has no ID
	if deployConfig.ID == "" {
		existingApp, err := svc.FindAppByName(deployConfig.App)
		if err == nil && existingApp != nil {
			fmt.Printf("An app named '%s' already exists (ID: %s).\n", deployConfig.App, existingApp.ID)
			fmt.Println("Run 'runos deploy sync' to link to existing app, or rename the app in runos.yaml.")
			return fmt.Errorf("app already exists - sync or rename required")
		}
	}

	// Pre-deploy drift gate: if the yaml is a pulled-app yaml (id/cid/aid set),
	// compare local against server state and refuse to deploy when local would
	// overwrite changes the user hasn't pulled. Pass --force to bypass.
	//
	// hasLegacy flips the refusal output to a "migrate via apps pull --force"
	// message when the local yaml still uses deprecated top-level fields
	// (port:, domain:, standardHttps:) that the server has migrated away from.
	force, _ := cmd.Flags().GetBool("force")
	hasLegacy := deploy.HasLegacyFields(deployConfig)
	if err := preDeployDriftCheck(cfg, token, cid, configPath, force, hasLegacy); err != nil {
		// We've already printed the diff + reconcile/migrate hints to
		// stdout. Cobra's default behaviour is to dump command usage on
		// any returned error, which here is just visual noise on top of
		// an already-rich refusal output.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}
	// Code drift gate: did anyone deploy via console / CI between this
	// directory's last pull (or last deploy) and now? If so, refuse so
	// the user can pull-and-rebase before overwriting upstream code.
	if err := preDeployCodeDriftCheck(cfg, token, cid, configPath, force); err != nil {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}

	// Pre-deploy sync: catch any console-side changes before deploying
	if _, err := syncAppState(svc, deployConfig, configPath, cid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy sync failed: %v\n", err)
	}

	// Surface a one-line nudge for users still on the pre-multi-yaml env
	// layout (.runos.<cid>.env). The fallback that loaded that file
	// silently was removed when multi-yaml support landed because two
	// apps in the same cluster + directory would silently share env vars.
	deploy.WarnLegacyEnv(configDir, deployConfig, cid)

	// Load env vars from env file AFTER sync so remote changes are included
	envPath, envConfigChanged := deploy.ResolveEnvPath(configDir, deployConfig, cid)
	if envConfigChanged {
		if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update config with env path: %v\n", err)
		}
	}
	customEnvVars, err := deploy.LoadEnvFile(envPath)
	if err != nil {
		return fmt.Errorf("failed to load env file: %w", err)
	}
	if customEnvVars != nil {
		deployConfig.CustomEnvVars = customEnvVars
	}

	fmt.Printf("Deploying %s...\n", deployConfig.App)

	// Prepare deployment
	fmt.Println("Preparing deployment...")
	prepResp, err := svc.PrepareDeployment(deployConfig)
	if err != nil {
		return fmt.Errorf("failed to prepare deployment: %w", err)
	}

	// Persist the IDs the prepare response just minted before the
	// upload starts; if the upload fails afterwards a re-run of deploy
	// links to the same app instead of creating a duplicate.
	if err := syncConfigFromPrepareResponse(deployConfig, configPath, prepResp, cid, cfg.AccountID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update config file: %v\n", err)
	}

	// Class-shorthand created services? Pull a service yaml to disk for
	// each one so the user ends up with proper IaC even when they used
	// the requires.<alias>.class quickstart. Best-effort: failures here
	// don't abort the deploy, since the conductor side has already
	// provisioned the service.
	if err := writeProvisionedServiceYAMLs(cfg, configDir, cid, prepResp); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// Resolve the build context. Defaults to the yaml's own directory
	// (configDir); when the yaml lives in a per-app subdirectory and
	// the source code is at the project root, the user sets
	// sourceDir: ".." so the tarball walks the right tree.
	archiveRoot, err := deploy.ResolveArchiveRoot(configDir, deployConfig.SourceDir)
	if err != nil {
		return fmt.Errorf("invalid sourceDir: %w", err)
	}

	// Create tarball
	fmt.Println("Creating archive...")
	tarball, err := deploy.CreateTarball(archiveRoot)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}

	fmt.Printf("Archive size: %d bytes\n", tarball.Len())

	// Upload tarball
	fmt.Println("Uploading archive...")
	if err := svc.UploadTarball(prepResp.UploadURL, prepResp.Token, tarball); err != nil {
		return fmt.Errorf("failed to upload archive: %w", err)
	}

	fmt.Println("Upload complete.")

	// Record the new source version so the next deploy / pull can
	// detect upstream drift relative to this deploy. Sidecar is per-app
	// (.runos.<cid>.<id>.source-version) so two apps in one directory
	// don't share an anchor.
	if sv := sourceVersionFromPrepare(prepResp); sv != "" {
		if err := apps.WriteSourceVersion(configDir, cid, deployConfig.ID, sv); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record source version: %v\n", err)
		}
	}

	// Output response
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		output, err := json.MarshalIndent(prepResp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal response: %w", err)
		}
		fmt.Println(string(output))
	} else {
		fmt.Printf("\nDeployment initiated:\n")
		fmt.Printf("  Job ID: %s\n", prepResp.JobID)
		fmt.Printf("  App ID: %s\n", prepResp.AppID)
	}

	// Follow job if requested
	follow, _ := cmd.Flags().GetBool("follow")
	if follow {
		fmt.Println("\nFollowing job progress...")
		if err := jobs.FollowJob(prepResp.JobID); err != nil {
			return fmt.Errorf("deployment failed: %w", err)
		}
		fmt.Println("\nDeployment completed successfully!")

		// Extract app ID for network access lookup
		appID := prepResp.AppID
		if appID == "" {
			appID = prepResp.OSID
		}

		// Fetch and display public URL
		networkAccess, err := svc.GetNetworkAccess(appID)
		if err == nil {
			for _, access := range networkAccess {
				if strings.HasPrefix(access.Type, "RUNOS_PUBLIC") {
					fmt.Printf("\nApp available at: %s\n", access.Link)
					break
				}
			}
		}
	}

	// Post-deploy env sync: pick up env vars from newly provisioned
	// services (also covers first deploy). Env handling has its own
	// conflict-detection UX so we keep it separate from the yaml,
	// which intentionally does NOT auto-refresh after deploy: the
	// conductor PATCH/build orchestration is async and a GET issued
	// here would race the Firestore write. Users who want the
	// server-applied defaults (clusterDomainId, resourceRequirementClassId,
	// requires.config, requires.env) on disk should run apps_pull.
	if _, err := syncAppState(svc, deployConfig, configPath, cid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-deploy sync failed: %v\n", err)
	}

	return nil
}

// syncConfigFromPrepareResponse persists the IDs the prepare response
// just minted (app id, deployType, service ids) before the upload runs.
// This is the safety net: if the upload fails after PrepareDeployment
// succeeded, the IDs are already on disk so a re-run of deploy links
// to the same app instead of creating a duplicate.
//
// All other server-applied state (cluster domain, resource class,
// requires.config / requires.env, normalised port mappings, etc.) is
// picked up by refreshYAMLAfterAction at the end of the deploy. That's
// the single round-trip that keeps the local yaml in lockstep with
// whatever the server actually accepted, so adding a new server-side
// default doesn't require another patch here.
func syncConfigFromPrepareResponse(deployConfig *deploy.DeployConfig, configPath string, prepResp *deploy.PrepareResponse, cid, aid string) error {
	deployConfig.ID = prepResp.AppID
	if deployConfig.ID == "" {
		deployConfig.ID = prepResp.OSID // Fallback to OSID if appId not set
	}
	deployConfig.CID = cid
	deployConfig.AID = aid
	// This is a CLI deploy, so the server tags the app with
	// deployType=cli. Mirror that in the local yaml so subsequent
	// pre-deploy diffs see the field on both sides and stop reporting
	// it as benign drift on every run.
	deployConfig.DeployType = "cli"

	// Update service IDs from the pre-generated services array.
	if prepResp.Services != nil && deployConfig.Requires != nil {
		for _, svc := range prepResp.Services {
			if req, ok := deployConfig.Requires[svc.Alias]; ok {
				req.ID = svc.ID
				deployConfig.Requires[svc.Alias] = req
			}
		}
	}

	// Class is creation-shorthand: the server reads it on the
	// first-create POST, ignores it on every subsequent update, and
	// the PATCH endpoint rejects it outright. Once a requires entry
	// has an id (i.e. the service exists), the id is the source of
	// truth and class no longer earns its keep in the yaml. Strip it
	// so re-pulls / re-deploys don't keep nudging the user toward a
	// field that does nothing. We strip here (rather than letting
	// refreshYAMLAfterAction do it) because the refresh's merge logic
	// preserves user-authored Class by design.
	for alias, req := range deployConfig.Requires {
		if req.ID != "" && req.Class != "" {
			req.Class = ""
			deployConfig.Requires[alias] = req
		}
	}

	return deploy.SaveConfig(configPath, deployConfig)
}

// writeProvisionedServiceYAMLs walks the prepare response's freshly
// provisioned services and writes a runos.service.<cid>.<sid>.yaml for
// each into configDir. This is the "creation shortcut produces IaC"
// rule: a deploy that used requires.<alias>.class to spin up a brand-
// new service ends up with a proper service yaml on disk so future
// changes go through services_sync.
//
// Skips services that already have a yaml on disk (idempotent re-runs)
// and services flagged as not new (already linked, not provisioned by
// this deploy). Errors are returned aggregated; the caller logs them as
// warnings since the conductor has already provisioned the services.
func writeProvisionedServiceYAMLs(cfg *config.Config, configDir, cid string, prepResp *deploy.PrepareResponse) error {
	if prepResp == nil || len(prepResp.Services) == 0 {
		return nil
	}
	// Only act on services this deploy actually created. Conductor sets
	// IsNew=true exactly when the prepare endpoint provisioned a fresh
	// service from a class-shorthand entry; existing-id requires keep it
	// false so we don't clobber a user's hand-edited service yaml.
	var fresh []deploy.ProvisionedService
	for _, s := range prepResp.Services {
		if s.IsNew && s.ID != "" && s.Type != "" {
			fresh = append(fresh, s)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	m, err := loadLocalManifest(cfg.GetAPIURL())
	if err != nil {
		return fmt.Errorf("write service yamls: %w", err)
	}
	exec := dynacmd.NewExecutor(cfg.GetAPIURL())
	var errs []string
	for _, s := range fresh {
		// Header-based lookup: a service yaml the user pulled and
		// renamed under any name still counts as "already on disk".
		if existing, ferr := services.FindByID(configDir, cid, s.ID); ferr == nil && existing != "" {
			continue
		}
		pulled, err := services.Pull(exec, m, s.Type, cid, cfg.AccountID, s.ID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: pull: %v", s.Type, s.ID, err))
			continue
		}
		dest := services.FilenameFor(configDir, cid, s.Type, s.ID)
		if err := services.Save(dest, pulled); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: save: %v", s.Type, s.ID, err))
			continue
		}
		fmt.Printf("Wrote service yaml: %s\n", dest)
	}
	if len(errs) > 0 {
		return fmt.Errorf("write service yamls (some failed):\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// syncResult holds what changed during a sync operation
type syncResult struct {
	deps    []deploy.AppDependency
	envVars map[string]string
}

// syncAppState syncs dependencies and env vars from the deployed app state.
// It updates the config and env file in place. Returns a result for summary printing.
func syncAppState(svc *deploy.Service, deployConfig *deploy.DeployConfig, configPath, cid string) (*syncResult, error) {
	if deployConfig.ID == "" {
		return nil, nil
	}

	configDir := filepath.Dir(configPath)
	result := &syncResult{}

	// Fetch and sync dependencies
	deps, err := svc.GetAppDependencies(deployConfig.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependencies: %v\n", err)
	} else {
		result.deps = deps
		if deps != nil && deployConfig.Requires != nil {
			for _, dep := range deps {
				if req, ok := deployConfig.Requires[dep.Name]; ok {
					if req.Type == dep.Type {
						req.ID = dep.ID
						deployConfig.Requires[dep.Name] = req
					}
				}
			}
		}
	}

	// Fetch and sync env vars
	envVars, err := svc.GetAppEnvVars(deployConfig.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch env vars: %v\n", err)
	} else {
		result.envVars = envVars
	}

	if len(envVars) > 0 {
		envPath, _ := deploy.ResolveEnvPath(configDir, deployConfig, cid)
		if envPath == "" {
			deployConfig.Env = deploy.DefaultEnvFilename(cid, deployConfig.ID)
			envPath = filepath.Join(configDir, deployConfig.Env)
		}

		localEnvVars, err := deploy.LoadEnvFile(envPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to read existing env file: %v\n", err)
		}

		// Check for conflicts: same key, different value
		var conflicts []string
		for key, localVal := range localEnvVars {
			if remoteVal, exists := envVars[key]; exists && localVal != remoteVal {
				conflicts = append(conflicts, key)
			}
		}
		if len(conflicts) > 0 {
			fmt.Fprintf(os.Stderr, "\nEnv var conflicts detected (local value differs from remote):\n")
			for _, key := range conflicts {
				fmt.Fprintf(os.Stderr, "  %s\n    local:  %s\n    remote: %s\n", key, localEnvVars[key], envVars[key])
			}
			return result, fmt.Errorf("resolve env var conflicts in %s before syncing", deployConfig.Env)
		}

		// Merge: start with remote vars, add any local-only vars
		merged := make(map[string]string, len(envVars)+len(localEnvVars))
		for k, v := range envVars {
			merged[k] = v
		}
		for k, v := range localEnvVars {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}

		if err := deploy.SaveEnvFile(envPath, merged); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save env file: %v\n", err)
		}
	}

	// Save config
	if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
		return result, fmt.Errorf("failed to save config: %w", err)
	}

	return result, nil
}

// sourceVersionFromPrepare picks the identifier to record in the
// per-app .runos-source-version sidecar after a successful upload.
// The id we want is the cliUploadID, the same string conductor returns
// from GET /apps/:id/cli-archives and accepts in prepare-cli-pull.
// Older conductors only return jobId from prepare-cli-deployment, so we
// fall back to that. When neither is set (older still, or partial
// response), we record nothing and the gate's drift check will simply
// have no baseline.
func sourceVersionFromPrepare(resp *deploy.PrepareResponse) string {
	if resp == nil {
		return ""
	}
	if resp.CliUploadID != "" {
		return resp.CliUploadID
	}
	return resp.JobID
}

// preDeployCodeDriftCheck refuses to deploy when the server has had
// CLI deploys after the cliUploadID recorded in the per-app sidecar
// file. The sidecar is written by `apps pull --code` and by every
// successful deploy, so its presence means "this directory was based
// on archive X". If newer archives exist on the server, deploying
// would overwrite whatever those archives shipped (most likely a
// teammate's deploy or a console-side push) with this directory's
// source.
//
// No sidecar = no baseline = skip silently. The check costs one API
// call (ListCliArchives). Pass force=true to bypass.
//
// This gate is fail-closed: if the archive listing API errors, we
// refuse the deploy. The deploy itself does NOT recheck for upstream
// archives, so a fail-open here would silently let through the very
// thing this gate exists to prevent (overwriting a teammate's deploy).
// Pass --force to deploy anyway when the API is genuinely unavailable.
func preDeployCodeDriftCheck(cfg *config.Config, token, cid, configPath string, force bool) error {
	localApp, err := apps.LoadLocalApp(configPath)
	if err != nil || localApp.ID == "" {
		// Yaml unparseable or fresh: same fail-open behaviour as
		// preDeployDriftCheck (no baseline to compare against).
		return nil
	}

	appsSvc := apps.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)
	status, err := apps.ComputeCodeVersionStatus(appsSvc, cid, localApp.ID, filepath.Dir(configPath))
	if err != nil {
		if force {
			fmt.Fprintf(os.Stderr, "Warning: pre-deploy code drift check failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "--force was passed, proceeding with deploy.")
			return nil
		}
		return fmt.Errorf("pre-deploy code drift check failed: %w (pass --force to deploy without checking)", err)
	}
	if status == nil {
		// No baseline (config-only pull or pre-sidecar yaml).
		return nil
	}
	if !status.RecordedFound {
		fmt.Fprintf(os.Stderr, "Note: recorded source version %s isn't in the server's archive list; skipping code drift check.\n", status.Recorded)
		return nil
	}
	if !status.IsStale() {
		return nil
	}

	if force {
		fmt.Fprintf(os.Stderr, "Warning: %d newer deploy(s) on the server since last pull, but --force was passed.\n", status.NewerCount)
		return nil
	}

	fmt.Printf("\n%s, %d newer deploy(s) on the server since this directory was last refreshed (cliUploadID %s):\n", localApp.App, status.NewerCount, status.Recorded)
	for _, a := range status.NewerArchives {
		fmt.Printf("  %s  %s\n", a.PushTime, a.CliUploadID)
	}
	fmt.Println()
	fmt.Println("Deploying now would replace whatever those deploys shipped with the source in this directory.")
	fmt.Printf("Reconcile:     runos apps pull %s --code --force\n", configPath)
	fmt.Printf("Inspect:       runos apps list-previous-uploads %s\n", configPath)
	fmt.Printf("Deploy anyway: runos deploy --force\n")
	return fmt.Errorf("upstream code drift detected; pass --force to deploy anyway")
}

// preDeployDriftCheck refuses to deploy when the local yaml has diverged
// from the running app on the server. It only runs when configPath
// parses as a pulled-app yaml with id/cid/aid set; fresh deploy yamls
// (no ids yet) skip the gate silently. Errors fetching server state
// surface as warnings rather than hard failures so the gate doesn't
// block deploys when the API is briefly unavailable, the deploy itself
// will fail loudly anyway. Pass force=true to bypass the gate entirely.
//
// hasLegacy customises the refusal output: when the local yaml uses
// deprecated top-level fields (port:, domain:, standardHttps:), the
// drift is almost always a side effect of the schema mismatch rather
// than real local edits. We surface a tailored "migrate via apps pull"
// recommendation so the user (and any LLM driving the deploy) picks
// the migration path instead of `--force`-ing onto the legacy shape.
func preDeployDriftCheck(cfg *config.Config, token, cid, configPath string, force, hasLegacy bool) error {
	localApp, err := apps.LoadLocalApp(configPath)
	if err != nil {
		// Yaml didn't parse as a pulled-app manifest. Two cases:
		//   1. Genuine bare deploy yaml that just doesn't carry the
		//      pulled-app shape (no id/cid/aid yet) — common, harmless.
		//   2. A pulled-app yaml with malformed content that happens to
		//      let DeployConfig.LoadConfig through but trips PulledApp.
		// We can't tell them apart from here. Surface a one-line note
		// so the user (and any LLM driving the deploy) sees that the
		// gate didn't run, then proceed — the deploy itself will fail
		// loudly if the yaml is genuinely broken.
		fmt.Fprintf(os.Stderr, "Note: pre-deploy drift gate skipped (yaml didn't parse as a pulled-app manifest: %v).\n", err)
		return nil
	}
	if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
		// Fresh deploy yaml: no upstream state to compare against.
		return nil
	}
	// Defence-in-depth: ids flow into URLs and (server-side) into
	// filesystem paths; reject anything outside the conductor identifier
	// alphabet so a tampered local yaml can't smuggle path components.
	if err := apps.ValidateIdentifier("app id", localApp.ID); err != nil {
		return fmt.Errorf("pre-deploy gate: %w", err)
	}
	if err := apps.ValidateIdentifier("cluster id", localApp.CID); err != nil {
		return fmt.Errorf("pre-deploy gate: %w", err)
	}

	appsSvc := apps.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)
	report, err := apps.BuildDiffReport(appsSvc, localApp, configPath, cfg.AccountID, cid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy drift check failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "Proceeding with deploy. Run 'runos apps diff %s' manually to verify.\n", configPath)
		return nil
	}
	// emitDeletionWarning surfaces server-only fields that a deploy
	// might clear. Under the new conductor's omit-equals-clear rule,
	// any of healthCheck / healthCheckPort / healthCheckPath /
	// metricsPort / metricsPath that the server has but the local
	// yaml omits will be DELETED on push. Other server-only fields
	// (replicas, clusterDomainId, etc.) are partial-update: the
	// server preserves them on omission. We surface every server-only
	// field rather than try to filter, so the user sees the same
	// list the diff renders and isn't surprised either way.
	emitDeletionWarning := func() {
		if len(report.YAML.ServerOnlyFields) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "WARNING: the server has fields your local yaml doesn't.")
		fmt.Fprintln(os.Stderr, "         healthCheck* and metrics* fields will be CLEARED by this deploy")
		fmt.Fprintln(os.Stderr, "         (omit-equals-clear). Other fields are preserved server-side.")
		fmt.Fprintln(os.Stderr)
		for _, f := range report.YAML.ServerOnlyFields {
			fmt.Fprintf(os.Stderr, "           - %s\n", f)
		}
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "         To keep them, cancel and run:")
		fmt.Fprintf(os.Stderr, "           runos apps pull %s --force\n", configPath)
		fmt.Fprintln(os.Stderr, "         which merges server state into your local yaml first.")
		fmt.Fprintln(os.Stderr)
	}

	if !report.NeedsForceToDeploy() {
		// Gate isn't refusing. Under the new desired-state model that
		// covers two friendly cases:
		//   - No drift at all (deploy is a no-op for state).
		//   - Drift is purely "local has fields server doesn't" — the
		//     user added something locally, deploy will set it.
		// In the second case, server-only fields could still appear on
		// nested values, so we still emit the deletion warning when
		// applicable. Always print the unified plan when there's drift
		// so the user sees exactly what's about to change.
		if report.HasDrift() {
			fmt.Printf("\n%s (%s) on cluster %s, deploy plan:\n", report.AppName, report.AppID, report.CID)
			printDiffReport(report)
			fmt.Println()
			emitDeletionWarning()
		}
		return nil
	}

	if force {
		emitDeletionWarning()
		// Force path covers two different scenarios under the new
		// model: server-only fields (clears on push) and divergent
		// values (overwrite on push). In both cases the diff above
		// shows what's about to happen; we only need a one-liner
		// preface so the user (or LLM) knows --force is in effect.
		fmt.Fprintln(os.Stderr, "Warning: server has changes your local yaml doesn't reflect, but --force was passed.")
		fmt.Fprintln(os.Stderr, "         Deploy will reconcile the server to match the local yaml.")
		if hasLegacy {
			fmt.Fprintln(os.Stderr, "         Note: this yaml uses deprecated fields (port:/domain:/standardHttps:).")
			fmt.Fprintln(os.Stderr, "         Forcing through means the same drift will reappear on every deploy.")
			fmt.Fprintf(os.Stderr, "         Recommended fix: runos apps pull %s --force\n", configPath)
		}
		fmt.Fprintln(os.Stderr)
		printDiffReport(report)
		fmt.Fprintln(os.Stderr)
		return nil
	}

	fmt.Printf("\n%s (%s) on cluster %s, the server has state your local yaml doesn't reflect.\n", report.AppName, report.AppID, report.CID)
	fmt.Println("Deploying now would overwrite changes that aren't in your local files.")
	printDiffReport(report)
	fmt.Println()
	if hasLegacy {
		fmt.Println("Your runos.yaml uses deprecated field names (top-level `port:`, `domain:`, or")
		fmt.Println("`standardHttps:`) that have been superseded by `servicePortMappings`. This is the")
		fmt.Println("most likely cause of the drift above; the server already stores the canonical shape.")
		fmt.Println()
		fmt.Println("RECOMMENDED — migrate the local yaml to the canonical format:")
		fmt.Printf("  runos apps pull %s --force\n", configPath)
		fmt.Println("Then re-run `runos deploy`. The migration is one-time per yaml.")
		fmt.Println()
		fmt.Println("Other options:")
		fmt.Printf("  Inspect:       runos apps diff %s\n", configPath)
		fmt.Printf("  Deploy anyway: runos deploy --force   (keeps the legacy shape; same drift\n")
		fmt.Println("                                        will reappear next deploy)")
	} else {
		fmt.Printf("Reconcile:  runos apps pull %s --force      (merge server state into your yaml first)\n", configPath)
		fmt.Printf("Inspect:    runos apps diff %s\n", configPath)
		fmt.Printf("Deploy anyway: runos deploy --force         (push your yaml; server state updates to match)\n")
	}
	return fmt.Errorf("upstream drift detected; pass --force to deploy anyway")
}

func runDeploySync(cmd *cobra.Command, args []string) error {
	// Load CLI config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get auth token (RUNOS_API_KEY env var, falling back to Firebase ID token).
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' or set RUNOS_API_KEY (%w)", err)
	}

	// Get cluster ID
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
	}

	// Get account ID (env var takes precedence so CI runs without a config file).
	aid := cfg.GetAccountID()
	if aid == "" {
		return fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	cfg.AccountID = aid

	// Load deploy config
	configPath, _ := cmd.Flags().GetString("config")
	if !filepath.IsAbs(configPath) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}
		configPath = filepath.Join(cwd, configPath)
	}

	deployConfig, err := deploy.LoadConfig(configPath)
	if err != nil {
		return err
	}

	// Validate AID
	if err := deploy.ValidateAID(deployConfig.AID, cfg.AccountID); err != nil {
		return err
	}

	// Create deploy service
	svc := deploy.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)

	// Find the app by name and set core IDs
	fmt.Printf("Looking up app '%s' on cluster %s...\n", deployConfig.App, cid)
	app, err := svc.FindAppByName(deployConfig.App)
	if err != nil {
		return fmt.Errorf("failed to find app: %w", err)
	}
	if app == nil {
		return fmt.Errorf("app '%s' not found on cluster %s. Run 'runos deploy' first", deployConfig.App, cid)
	}

	deployConfig.ID = app.ID
	deployConfig.CID = cid
	deployConfig.AID = cfg.AccountID

	// Sync dependencies and env vars
	fmt.Println("Syncing app state...")
	result, err := syncAppState(svc, deployConfig, configPath, cid)
	if err != nil {
		return err
	}

	// Print summary
	fmt.Printf("\nConfig file updated:\n")
	fmt.Printf("  App ID: %s\n", deployConfig.ID)
	fmt.Printf("  Cluster: %s\n", deployConfig.CID)
	fmt.Printf("  Account: %s\n", deployConfig.AID)
	if result != nil && len(result.deps) > 0 {
		fmt.Println("  Dependencies:")
		for _, dep := range result.deps {
			fmt.Printf("    %s (%s): %s\n", dep.Name, dep.Type, dep.ID)
		}
	}
	if result != nil && len(result.envVars) > 0 {
		fmt.Printf("  Env vars: %d synced to %s\n", len(result.envVars), deployConfig.Env)
	}

	return nil
}
