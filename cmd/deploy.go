package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/envfile"
	"github.com/runos-official/cli/internal/git"
	"github.com/runos-official/cli/internal/jobs"
	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy an app from the current directory",
	Long: `Deploy an application to a RunOS cluster.

This command reads a runos.yaml configuration file from the current directory,
creates a tarball of the project files, and deploys it to the specified cluster.

The runos.yaml file should contain at minimum:
  app: "My App Name"
  servicePortMappings:
    - port: 8080
      standardHttps: true

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
	deployCmd.Flags().BoolP("follow", "f", false, "follow job progress until completion; without it, deploy prints the job ID and exits 0 the moment the conductor accepts the request")
	deployCmd.Flags().BoolP("json", "j", false, "output response as JSON")
	deployCmd.Flags().Bool("force", false, "deploy even when local diverges from the server (skips the pre-deploy drift gate)")
	deployCmd.Flags().BoolP("yes", "y", false, "skip the deploy confirmation prompt (auto-skipped when stdin is not a terminal, e.g. CI)")
	// VCS-only flags. --app targets a VCS app without needing runos.yaml on
	// disk (CI mode); --sha pins the commit to deploy (defaults to HEAD when
	// run from inside a git repo); --allow-dirty waives the dirty-tree refusal.
	deployCmd.Flags().String("app", "", "VCS-only: app ID to deploy (when no runos.yaml is present)")
	deployCmd.Flags().String("sha", "", "VCS-only: commit SHA to deploy (defaults to git rev-parse HEAD)")
	deployCmd.Flags().Bool("allow-dirty", false, "VCS-only: deploy even when the working tree has uncommitted changes")

	// Add sync subcommand
	deploySyncCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file")
	deploySyncCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	deployCmd.AddCommand(deploySyncCmd)
}

func runDeploy(cmd *cobra.Command, args []string) (rerr error) {
	// SilenceUsage right away so any early-stage error (config load,
	// yaml parse, cid resolution, ...) doesn't dump the full cobra help
	// block after the diagnostic. I7-G regression target.
	cmd.SilenceUsage = true
	// Route any returned error through the JSON envelope when --json is
	// set so the failure path matches the success path's shape. Pairs
	// with the API-error envelope in dynacmd's executor; without it,
	// parse-stage errors bypassed the contract and emitted human-only
	// text to stderr. I7-G regression target.
	jsonOutput, _ := cmd.Flags().GetBool("json")
	defer func() {
		if jsonOutput && rerr != nil {
			rerr = emitJSONError(cmd, rerr)
		}
	}()
	// Under --json, route every human-readable progress line to stderr
	// so stdout stays pure JSON (consumable by `jq` on a CI gate).
	// Pre-fix the preamble ("Deploying ...", "Preparing deployment...",
	// "Creating archive...", "Upload complete.") was emitted to stdout
	// before the JSON envelope, breaking `jq` parsing. The follow path
	// after the envelope keeps using stdout per its own streaming
	// contract; the test agent's I10-L was specifically about the
	// preamble. Regression target: I10-L.
	progress := func(format string, args ...any) {
		if jsonOutput {
			fmt.Fprintf(os.Stderr, format, args...)
			return
		}
		fmt.Printf(format, args...)
	}

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

	// Resolve cluster ID. Three sources in priority order:
	//   1. --cid flag
	//   2. CLI config default (`runos config set cid <id>`)
	//   3. yaml's `cid:` field (loaded below; lets a checked-in
	//      runos.yaml carry its target cluster without per-machine config).
	// Source 3 only applies when running with a yaml on disk; the --app /
	// CI-mode path errors immediately if neither flag nor default are set.
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}

	// Get account ID (env var takes precedence so CI runs without a config file).
	aid := cfg.GetAccountID()
	if aid == "" {
		return fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	cfg.AccountID = aid

	// VCS-deploy dispatch. Two paths into the VCS flow:
	//
	//   1. CI: `runos deploy --app <id> --sha <sha>`. No runos.yaml on disk;
	//      we trust the flag and fetch the app's deployType from the API
	//      to make sure we're not accidentally invoking VCS-deploy on a
	//      CLI-deploy app.
	//
	//   2. Laptop: `runos deploy` from a checkout of a VCS app's repo.
	//      runos.yaml is loaded as today; we branch when its deployType
	//      (or the server-side fallback) is 'vcs'.
	//
	// CLI-deploy apps reject --sha / --allow-dirty so the two modes never
	// silently intermingle.
	flagApp, _ := cmd.Flags().GetString("app")
	flagSha, _ := cmd.Flags().GetString("sha")
	flagAllowDirty, _ := cmd.Flags().GetBool("allow-dirty")
	flagFollow, _ := cmd.Flags().GetBool("follow")
	flagYes, _ := cmd.Flags().GetBool("yes")

	if flagApp != "" {
		// CI mode: --app pins the target, no yaml is consulted, so cid must
		// come from flag/config — yaml fallback is unavailable here.
		if cid == "" {
			return fmt.Errorf("cluster ID required when using --app: pass --cid or set default with 'runos config set cid <cluster-id>'")
		}
		svc := deploy.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)
		// Verify deployType server-side so we don't try to VCS-deploy a
		// CLI-deploy app.
		app, err := svc.GetApp(flagApp)
		if err != nil {
			return fmt.Errorf("failed to look up app %s: %w", flagApp, err)
		}
		if app == nil {
			return fmt.Errorf("app %s not found on cluster %s", flagApp, cid)
		}
		if app.DeployType != "vcs" {
			return fmt.Errorf("--app is only valid for VCS-deployed apps; %s has deployType=%q", flagApp, app.DeployType)
		}
		cmd.SilenceUsage = true
		// CI mode: no yaml on disk, so we don't auto-derive configPath here.
		// Conductor falls back to whatever the AppDocument has stored
		// (typically set by an earlier laptop deploy that DID send it).
		return runDeployVCS(svc, flagApp, flagSha, "", flagAllowDirty, flagFollow, flagYes, jsonOutput)
	}

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
		// When the user didn't override -c and the default `runos.yaml`
		// is missing, auto-detect pulled/fresh yamls in cwd. A unique
		// hit is auto-picked (so a per-app dir with only
		// `runos.<cid>.<id>.yaml` "just works"); multiple hits surface
		// the candidate list mirroring `apps diff`'s auto-detect, which
		// is the I15-A fix. Zero hits falls through to LoadConfig's
		// original "runos.yaml not found at <path>" message.
		if !cmd.Flags().Changed("config") {
			detected, detectErr := autoDetectDeployYAML(filepath.Dir(configPath))
			if detectErr != nil {
				return detectErr
			}
			if detected != "" {
				configPath = detected
				deployConfig, err = deploy.LoadConfig(configPath)
				if err != nil {
					return err
				}
			} else {
				return err
			}
		} else {
			return err
		}
	}

	// Yaml provides the third cid source: a checked-in runos.yaml that
	// already carries `cid:` shouldn't need an extra flag/config to deploy.
	// ReconcileCID also cross-checks when BOTH sources are set, refusing on
	// mismatch the same way `apps diff` does (I18-B: pre-fix the flag
	// silently won and a stale --cid against a directory-per-app yaml could
	// push to the wrong cluster).
	cid, err = deploy.ReconcileCID(cid, deployConfig.CID)
	if err != nil {
		cmd.SilenceUsage = true
		return err
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: pass --cid, run 'runos config set cid <cluster-id>', or include cid: in %s", configPath)
	}

	// Validate AID
	if err := deploy.ValidateAID(deployConfig.AID, cfg.AccountID); err != nil {
		return err
	}

	svc := deploy.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)

	// Laptop VCS path: if the loaded runos.yaml is for a VCS app, branch into
	// the VCS flow. We trust deployConfig.DeployType when present (set by
	// `apps pull` or a previous deploy); otherwise fall back to the server.
	deployType := deployConfig.DeployType
	if deployType == "" && deployConfig.ID != "" {
		if app, err := svc.GetApp(deployConfig.ID); err == nil && app != nil {
			deployType = app.DeployType
		}
	}

	if deployType == "vcs" {
		cmd.SilenceUsage = true
		configPathForServer := resolveVcsConfigPath(deployConfig, configPath)
		return runDeployVCS(svc, deployConfig.ID, flagSha, configPathForServer, flagAllowDirty, flagFollow, flagYes, jsonOutput)
	}

	// CLI-deploy guard: the VCS-only flags must not silently no-op here.
	if flagSha != "" || flagAllowDirty {
		return fmt.Errorf("--sha and --allow-dirty are only valid for VCS-deployed apps; this app has deployType=%q", deployType)
	}

	// Resolve config dir for downstream env-file lookups, etc. The deploy
	// service was already constructed earlier (svc) for the VCS-deploy branch
	// and is reused below.
	configDir := filepath.Dir(configPath)

	// Check if app already exists but config has no ID
	if deployConfig.ID == "" {
		existingApp, err := svc.FindAppByName(deployConfig.App)
		if err == nil && existingApp != nil {
			// I25-M: a hand-authored yaml with only `app: <name>` and no
			// `id:` / `deployType:` routes here. When the matching server
			// app is VCS-shaped, the legacy "Run 'runos deploy sync' to
			// link" hint is wrong (deploy sync is a CLI-deploy verb).
			// Steer the user to either add the VCS fields to the yaml or
			// drop into the laptop-vcs flow directly.
			// I27-P: VCS-conflict guidance routes through progress() so it
			// lands on stderr under --json. The defer'd emitJSONError then
			// writes the JSON envelope to stdout alone (pure JSON for `jq`).
			if existingApp.DeployType == "vcs" {
				progress("An app named '%s' already exists on this cluster as a VCS-deployed app (ID: %s).\n", deployConfig.App, existingApp.ID)
				progress("Hand-authored yaml is missing the VCS shape. Either:\n")
				progress("  1. Pull the canonical yaml: `runos apps pull --id %s`\n", existingApp.ID)
				progress("  2. Or add `id: %s` and `deployType: vcs` to runos.yaml and re-run\n", existingApp.ID)
				progress("  3. Or deploy directly: `runos deploy --app %s --sha <sha>`\n", existingApp.ID)
				return fmt.Errorf("app already exists as VCS-deployed; yaml is missing id + deployType")
			}
			progress("An app named '%s' already exists (ID: %s).\n", deployConfig.App, existingApp.ID)
			progress("Run 'runos deploy sync' to link to existing app, or rename the app in runos.yaml.\n")
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
	if err := preDeployDriftCheck(cfg, token, cid, configPath, force, hasLegacy, jsonOutput); err != nil {
		// We've already printed the diff + reconcile/migrate hints to
		// stdout (or stderr under --json). Cobra's default behaviour is
		// to dump command usage on any returned error, which here is
		// just visual noise on top of an already-rich refusal output.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}
	// Code drift gate: did anyone deploy via console / CI between this
	// directory's last pull (or last deploy) and now? If so, refuse so
	// the user can pull-and-rebase before overwriting upstream code.
	if err := preDeployCodeDriftCheck(cfg, token, cid, configPath, force, jsonOutput); err != nil {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}
	// Domain-removal gate (I2-4e): if the local yaml drops a `domain:`
	// or `servicePortMappings[].domains[].fqdn` that the server still
	// has attached to this app, conductor's reconciler will silently
	// remove the mapping (along with any provider-managed DNS record).
	// Prompt the user before proceeding. Auto-skipped when stdin is
	// not a terminal (CI), --yes is set, or --force is set.
	if err := preDeployDomainRemovalGate(svc, deployConfig, force, flagYes); err != nil {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}

	// Pre-deploy sync: pulls server-side env vars DOWN into the local files
	// so the deploy body the CLI POSTs reflects whatever the console (or a
	// teammate) set since the last local refresh. Local-wins merge:
	//
	//   - Server-only keys: added to the local file. Captured in
	//     preDeploySync.{secret,env}LocalMissingServerHas so warnLocalDeletions
	//     can flag the case-E surprise at end of deploy: the user *deleted*
	//     a key locally and this pull silently re-added it, so the deploy
	//     also re-pushed it (deploy never deletes server keys; `runos apps
	//     sync` is the replace-all verb that does).
	//   - Same key, different value: local kept verbatim. The deploy that
	//     follows then pushes the local value, replacing the server's. This
	//     is the dominant path (every version bump) and is correctly
	//     silent — the user edited a value, deploy pushed it, no surprise
	//     to surface.
	//
	// Error return here is reserved for actual I/O failures (file write
	// errors); divergence between local and server is no longer an error.
	preDeploySync, err := syncAppState(svc, deployConfig, configPath, cid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy sync failed: %v\n", err)
	}

	// Surface a one-line nudge for users still on the pre-multi-yaml env
	// layout (.runos.<cid>.env). The fallback that loaded that file
	// silently was removed when multi-yaml support landed because two
	// apps in the same cluster + directory would silently share env vars.
	deploy.WarnLegacyEnv(configDir, deployConfig, cid)

	// Load env vars from both env files AFTER sync so remote changes are
	// included. Two parallel sources:
	//  - secret env file (.runos.{cid}.{id}.env): sensitive, gitignored;
	//    populates customSecretEnvVars and lands in the K8s Secret.
	//  - plain env file (runos.{cid}.{id}.config.env): committed to VCS;
	//    populates customEnvVars and lands in the K8s ConfigMap.
	envPaths, envConfigChanged := deploy.ResolveEnvFiles(configDir, deployConfig, cid)
	if envConfigChanged {
		if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update config with env paths: %v\n", err)
		}
	}
	customSecretEnvVars, err := deploy.LoadEnvFile(envPaths.Secret)
	if err != nil {
		return fmt.Errorf("failed to load secret env file: %w", err)
	}
	if err := envfile.Validate(customSecretEnvVars); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(envPaths.Secret), err)
	}
	if customSecretEnvVars != nil {
		deployConfig.CustomSecretEnvVars = customSecretEnvVars
	}
	customEnvVars, err := deploy.LoadEnvFile(envPaths.Plain)
	if err != nil {
		return fmt.Errorf("failed to load env file: %w", err)
	}
	if err := envfile.Validate(customEnvVars); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(envPaths.Plain), err)
	}
	if customEnvVars != nil {
		deployConfig.CustomEnvVars = customEnvVars
	}

	// Pre-flight conflict check. The conductor enforces this server-side
	// (and refuses the deploy with a 400), but failing fast on the client
	// keeps the error closer to the user's edit.
	if conflicts := envKeyConflicts(customSecretEnvVars, customEnvVars); len(conflicts) > 0 {
		return fmt.Errorf(
			"env-var keys appear in both %s and %s: %s. "+
				"Move each key to exactly one file (typically secret values stay in the .env file, plain values move to the .config.env file).",
			filepath.Base(envPaths.Secret), filepath.Base(envPaths.Plain), strings.Join(conflicts, ", "),
		)
	}

	// Plain-side platform-claimed names: hard refusal. A `DATABASE_URL=...`
	// committed to VCS while the platform also injects one is silently
	// ignored at runtime (the requires-merge wins) and is the kind of
	// "looks right, totally wrong" trap that quietly persists in Git
	// history. The secret-side equivalent is harmless (apps_pull writes
	// it there and the merge re-runs on every deploy).
	if collisions := deployRequiresEnvCollisions(deployConfig, customEnvVars); len(collisions) > 0 {
		names := make([]string, 0, len(collisions))
		for _, c := range collisions {
			names = append(names, fmt.Sprintf("%s (claimed by requires.%s.env.%s)", c.EnvVar, c.Alias, c.Field))
		}
		return fmt.Errorf(
			"plain env file %s contains platform-claimed names: %s. "+
				"These are injected by the platform from the linked service's credentials, "+
				"so a hand-authored value would be ignored at runtime and committed to VCS by mistake. "+
				"Remove them from %s (the platform-injected values land in the secret file %s automatically).",
			filepath.Base(envPaths.Plain), strings.Join(names, ", "),
			filepath.Base(envPaths.Plain), filepath.Base(envPaths.Secret),
		)
	}

	// Detect names in the local secret env file that the platform claims
	// via requires.<alias>.env (DATABASE_URL, REDIS_HOST, etc.). These
	// are NOT a problem on the secret side: apps_pull legitimately writes
	// them so the local file matches the K8s Secret, and they self-sync
	// across deploys. We track them here only to filter the post-deploy
	// "got merged back" warning below — for platform-claimed keys, the
	// re-merge is the design, not a deletion that didn't take effect.
	// (The plain-side equivalent — committing platform-claimed credentials
	// to VCS — is a real footgun, but conductor's secret/plain conflict
	// gate hard-refuses that on push so we don't need a CLI-side warning
	// for it.)
	requiresEnvCollisions := deployRequiresEnvCollisions(deployConfig, customSecretEnvVars)

	// Resolve the build context now (cheap, just filepath.Join + os.Stat) so
	// we can show it in the confirm prompt. Defaults to the yaml's own
	// directory (configDir); when the yaml lives in a per-app subdirectory
	// and the source code is at the project root, the user sets
	// sourceDir: ".." so the tarball walks the right tree.
	archiveRoot, err := deploy.ResolveArchiveRoot(configDir, deployConfig.SourceDir)
	if err != nil {
		return fmt.Errorf("invalid sourceDir: %w", err)
	}
	// I27-Y pre-flight: the dockerfile lives at <archiveRoot>/<dockerfile>
	// on the build server (BuildKit's --opt filename is relative to the
	// archive root). A misconfigured field (typo, or path relative to the
	// yaml's own dir instead of sourceDir) used to silently upload and
	// fail server-side with `failed to read dockerfile: open <name>: no
	// such file or directory`. Validate locally before any network call.
	dockerfileAbs, err := deploy.ResolveDockerfilePath(archiveRoot, deployConfig.Dockerfile)
	if err != nil {
		return fmt.Errorf("invalid dockerfile: %w", err)
	}
	// I27-G: peek at the Dockerfile's base image. RunOS clusters reject
	// containers running as root, so the bare `nginx:alpine` image (binds
	// port 80, requires root) lands in CrashLoopBackOff after every deploy.
	// Emit a stderr advisory naming the unprivileged variant as the drop-in
	// fix; non-blocking so the deploy still proceeds (some users have
	// patched the base image themselves to drop root, no need to refuse).
	if hint := deploy.NginxDockerfileHint(dockerfileAbs); hint != "" {
		fmt.Fprintln(os.Stderr, hint)
	}

	// Confirmation gate: shows the user what's about to be deployed and
	// requires explicit y/yes before any work begins. Auto-skipped when
	// stdin is not a terminal (CI / piped input) or when --yes is set.
	// Last chance to catch "ran in the wrong tab" / "wrong cluster" before
	// we kick off prepare-deployment + tarball + upload.
	if err := confirmDeploy(buildCLIDeploySummary(deployConfig.App, cid, cfg.AccountID, archiveRoot), flagYes); err != nil {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return err
	}

	progress("Deploying %s...\n", deployConfig.App)

	// Load secretFiles[].local file bytes (base64-encoded) into each
	// entry's Content field so PrepareDeployment's marshal carries the
	// conductor's expected wire shape `{filename, mountPath, content}`.
	// Conductor R2 wires the receive + orchestration end-to-end via the
	// new "Apply user secret files" step (update-only); without this
	// CLI-side load the wire body has only filename + mountPath and
	// the conductor's normalizeYaml treats the entries as malformed.
	// First-deploy users still need apps_secret-files_update because
	// the orchestration step is update-only. Regression target: I10-K
	// CLI half (wire-side flip).
	if err := deployConfig.LoadSecretFileContents(configDir); err != nil {
		return fmt.Errorf("failed to load secret file contents: %w", err)
	}

	// Prepare deployment
	progress("Preparing deployment...\n")
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
	// provisioned the service. 404s from a still-async create land in
	// the deferred bucket and are retried after the deploy job
	// completes (see flagFollow path below).
	freshSvcResult := writeProvisionedServiceYAMLs(cfg, configDir, cid, prepResp)
	if len(freshSvcResult.failed) > 0 {
		fmt.Fprintf(os.Stderr, "Warning: write service yamls (some failed):\n  %s\n", strings.Join(freshSvcResult.failed, "\n  "))
	}

	// Post-deploy IaC artifacts: write the same `.dockerignore` template
	// that `apps_pull` writes (skipped if one exists) and create the
	// `runos.<cid>.<id>.config.env` placeholder so the yaml's `env:` ref
	// points at a real file. Pre-fix, a user who only ran `runos deploy`
	// (the documented happy path) ended up with a yaml that referenced a
	// non-existent file and had no `.dockerignore` to protect external
	// `docker build` invocations from leaking RunOS-managed config into
	// images. Errors are warnings; the deploy itself doesn't roll back.
	if appID := chooseDeployAppID(deployConfig, prepResp); appID != "" {
		// Honor an explicit `env: <path>` in the local yaml (I3-A).
		// ResolveEnvFiles populates deployConfig.Env earlier; on a
		// genuinely-fresh deploy that ran with no app id at the time
		// of resolution, fall back to the canonical default keyed on
		// the freshly-minted appID.
		envFilename := deployConfig.Env
		if envFilename == "" {
			envFilename = apps.EnvFilename(cid, appID)
		}
		writeDeployIaCArtifacts(configDir, envFilename)
	}

	// Create tarball
	progress("Creating archive...\n")
	tarball, err := deploy.CreateTarball(archiveRoot)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}

	progress("Archive size: %d bytes\n", tarball.Len())

	// Upload tarball
	progress("Uploading archive...\n")
	if err := svc.UploadTarball(prepResp.UploadURL, prepResp.Token, tarball); err != nil {
		return fmt.Errorf("failed to upload archive: %w", err)
	}

	progress("Upload complete.\n")

	// Record the new source version so the next deploy / pull can
	// detect upstream drift relative to this deploy. Sidecar is per-app
	// (.runos.<cid>.<id>.source-version) so two apps in one directory
	// don't share an anchor.
	//
	// Capture the prior recorded version first: when --follow is passed
	// and the deploy job fails (e.g. build failure), we restore the
	// prior value so the recorded source-version still reflects the
	// last successfully-deployed code rather than a UUID whose image
	// was never produced. Fire-and-forget deploys can't observe
	// success/failure here, so they keep the new value (still useful:
	// `apps pull --code <uuid>` works because the archive uploaded).
	priorSourceVersion, _ := apps.ReadSourceVersion(configDir, cid, deployConfig.ID)
	newSourceVersion := sourceVersionFromPrepare(prepResp)
	if newSourceVersion != "" {
		if err := apps.WriteSourceVersion(configDir, cid, deployConfig.ID, newSourceVersion); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record source version: %v\n", err)
		}
	}

	// Output response (jsonOutput is captured at the top of runDeploy)
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
		// Console URL: gives the user a direct link to inspect the app
		// (logs, builds, env, requires) in the web console without
		// stitching the URL together by hand. Mirrors the documented
		// post-deploy contract.
		appID := prepResp.AppID
		if appID == "" {
			appID = prepResp.OSID
		}
		if appID != "" && cfg != nil {
			if consoleURL := strings.TrimRight(cfg.GetConsoleURL(), "/"); consoleURL != "" {
				fmt.Printf("  Console:    %s/%s/%s/applications/manage/%s\n",
					consoleURL, cfg.GetAccountID(), cid, appID)
			}
		}
		// Fire-and-forget mode only: surface the deferred yaml hint
		// here. Under --follow the retryDeferredServiceYAMLs call in
		// the success branch below materialises the yamls itself and
		// emits `Wrote service yaml: ...` for each, so this note would
		// be a misleading duplicate (I2-1b' papercut, TEST_LOG.md).
		if !flagFollow && len(freshSvcResult.deferred) > 0 {
			ids := make([]string, 0, len(freshSvcResult.deferred))
			for _, s := range freshSvcResult.deferred {
				ids = append(ids, s.Type+"/"+s.ID)
			}
			fmt.Printf("\nNote: yamls for newly-provisioned %s will materialise on the next `runos apps pull --force`.\n",
				strings.Join(ids, ", "))
		}
	}

	// Follow job if --follow was passed. Default is fire-and-forget so the
	// user has predictable, opt-in control: the CLI never blocks unless
	// asked. CI workflows that want exit codes to gate downstream steps
	// add --follow explicitly.
	if flagFollow {
		fmt.Println("\nFollowing job progress...")
		if err := jobs.FollowJob(prepResp.JobID); err != nil {
			// Roll back the source-version sidecar so the recorded id
			// keeps pointing at the last successfully-deployed code.
			// Without this, a failed build leaves the sidecar pointing
			// at a UUID whose image was never produced, and the next
			// `runos deploy` (or drift gate) would treat the failure
			// as the new baseline. Restore the prior value (or remove
			// the file when there was no prior baseline). Best-effort:
			// any I/O failure is logged as a warning, not propagated.
			if newSourceVersion != "" && newSourceVersion != priorSourceVersion {
				if priorSourceVersion != "" {
					if rerr := apps.WriteSourceVersion(configDir, cid, deployConfig.ID, priorSourceVersion); rerr != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to restore source version after build failure: %v\n", rerr)
					}
				} else {
					if rerr := os.Remove(apps.SourceVersionPath(configDir, cid, deployConfig.ID)); rerr != nil && !os.IsNotExist(rerr) {
						fmt.Fprintf(os.Stderr, "Warning: failed to clear source version after build failure: %v\n", rerr)
					}
				}
			}
			return fmt.Errorf("deployment failed: %w", err)
		}
		fmt.Println("\nDeployment completed successfully!")

		// Retry any service yaml writes that 404'd at prepare-time
		// because the conductor's service-create work was still async.
		// By the time --follow returns success the resources have
		// settled, so the show endpoint should be 200.
		retryDeferredServiceYAMLs(cfg, configDir, cid, freshSvcResult.deferred)

		// I2-1d: AppDocument is settled now, so it's safe to fetch the
		// synthesized RRC and stamp it on the local yaml without
		// racing the deploy job's Firestore writes.
		stampSynthesizedResources(svc, deployConfig, configPath)

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
	// server-applied defaults (clusterDomainId, requires.config,
	// requires.env) on disk should run apps_pull.
	if _, err := syncAppState(svc, deployConfig, configPath, cid); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: post-deploy sync failed: %v\n", err)
	}

	// I2-1d: stamp the synthesised resource class on the local yaml.
	// Conductor's resolveRRC fills in a default class (e.g.
	// app.sl1.beff) on first deploy when the user has none set; absent
	// this stamp, the local yaml stays bare and `apps diff` reports
	// in_sync because the field is preserve-on-omit, leaving the user
	// no transparency into which class their app actually runs under.
	// Only fills absent local fields; user-set values are never
	// overwritten. Fire-and-forget mode opts out (the GET would race
	// the conductor's Firestore write before the deploy job touches
	// the AppDocument); --follow waits for terminal state and runs
	// later in the success branch instead. This pre-block path keeps
	// the env / requires sync ordering identical to before.
	if !flagFollow {
		stampSynthesizedResources(svc, deployConfig, configPath)
	}

	// Last thing printed: any local-env vs server-env warnings, ordered
	// from "specifically wrong" to "generally additive." Putting them
	// after the deploy summary makes them the final block of output an
	// MCP-driven LLM reads, much harder to gloss over than a banner that
	// scrolls off above the upload + summary.

	// Generic safety net: keys the user removed from a local env file that
	// the pre-deploy syncAppState merge silently re-introduced from server.
	// Deploy's env handling is additive only, so the deletion silently
	// doesn't take effect. Filter out platform-claimed keys (managed by
	// requires.<alias>.env) — those re-appear by design, not by mistake,
	// so warning the user about them is just noise. Reported per-side
	// because the two files (.runos.{cid}.{id}.env and
	// runos.{cid}.{id}.config.env) are independent.
	requiresEnvNames := make(map[string]bool, len(requiresEnvCollisions))
	for _, c := range requiresEnvCollisions {
		requiresEnvNames[c.EnvVar] = true
	}
	if preDeploySync != nil {
		warnLocalDeletions(envPaths.Secret, preDeploySync.secretLocalMissingServerHas, requiresEnvNames)
		warnLocalDeletions(envPaths.Plain, preDeploySync.envLocalMissingServerHas, nil)
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

// chooseDeployAppID returns the app id to use for post-deploy artifact
// writes. Prefers the deploy config's existing id (set on the second
// deploy onward); falls back to the prepare response's app id (first
// deploy). Returns "" when neither is available so the caller can skip
// artifact writes silently.
func chooseDeployAppID(deployConfig *deploy.DeployConfig, prepResp *deploy.PrepareResponse) string {
	if deployConfig != nil && deployConfig.ID != "" {
		return deployConfig.ID
	}
	if prepResp != nil {
		if prepResp.AppID != "" {
			return prepResp.AppID
		}
		if prepResp.OSID != "" {
			return strings.TrimPrefix(prepResp.OSID, "app-")
		}
	}
	return ""
}

// writeDeployIaCArtifacts writes the post-deploy IaC artifacts the user
// would otherwise get only after running `apps_pull --force` once: the
// `.dockerignore` template (covers external `docker build` invocations)
// and a placeholder env file at envFilename (so the yaml's `env:`
// reference points at a real file rather than dangling). Both writes
// are idempotent: existing files are left untouched. envFilename is
// the path resolved against the local yaml (caller honours an explicit
// `env:` field; falls back to the canonical
// `runos.<cid>.<id>.config.env` when absent), so a user with
// `env: plain.env` doesn't accumulate a dead canonical sibling
// alongside the real file (I3-A). Errors print as warnings; the deploy
// doesn't roll back since the conductor side has already accepted the
// request.
func writeDeployIaCArtifacts(configDir, envFilename string) {
	if dr, err := apps.EnsureDockerignore(configDir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write .dockerignore: %v\n", err)
	} else if !dr.InSync {
		fmt.Printf("Wrote .dockerignore: %s\n", dr.Path)
	}
	if envFilename == "" {
		return
	}
	envPath := filepath.Join(configDir, envFilename)
	if _, err := os.Stat(envPath); err == nil {
		return
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: failed to stat %s: %v\n", envPath, err)
		return
	}
	header := "# Plain ConfigMap-backed env vars for this app.\n" +
		"# Committed to VCS, never put credentials here (use the secret env\n" +
		"# file instead, which is gitignored). Lines are KEY=value, # comments allowed.\n"
	if err := os.WriteFile(envPath, []byte(header), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to write %s: %v\n", envPath, err)
		return
	}
	fmt.Printf("Wrote env file: %s\n", envPath)
}

// writeProvisionedServiceYAMLsResult breaks the per-service outcome into
// three buckets so callers can react sensibly:
//   - written: service yamls successfully landed on disk this call.
//   - deferred: services the conductor accepted but whose show endpoint
//     still 404s (the create job is async). Common for first-deploy of a
//     brand-new mysql/valkey/etc. through requires.<alias>.class. Caller
//     retries these after the deploy job completes.
//   - failed: anything else (non-404 pull error, save error). These do
//     surface as warnings.
type writeProvisionedServiceYAMLsResult struct {
	written  []string
	deferred []deploy.ProvisionedService
	failed   []string
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
// this deploy). 404s on the show endpoint are routed to the deferred
// bucket because the conductor's service-create work runs async and the
// yaml becomes pullable later in the deploy lifecycle. The caller is
// expected to retry deferred entries via retryDeferredServiceYAMLs once
// the deploy job is complete (--follow mode) or to inform the user that
// `runos apps pull --force` will materialise them later.
func writeProvisionedServiceYAMLs(cfg *config.Config, configDir, cid string, prepResp *deploy.PrepareResponse) writeProvisionedServiceYAMLsResult {
	var res writeProvisionedServiceYAMLsResult
	if prepResp == nil || len(prepResp.Services) == 0 {
		return res
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
		return res
	}
	m, err := loadLocalManifest(cfg.GetAPIURL())
	if err != nil {
		res.failed = append(res.failed, fmt.Sprintf("load manifest: %v", err))
		return res
	}
	exec := dynacmd.NewExecutor(cfg.GetAPIURL())
	repoRoot, _ := git.RepoRoot()
	for _, s := range fresh {
		if existing := services.ExistingServiceYamlPath(repoRoot, configDir, cid, s.ID); existing != "" {
			continue
		}
		pulled, err := services.Pull(exec, m, s.Type, cid, cfg.AccountID, s.ID)
		if err != nil {
			if isAPINotFound(err) {
				// Conductor accepted the create but the service show
				// endpoint isn't 200 yet. Defer the yaml write until
				// after the deploy job, when the resource has settled.
				res.deferred = append(res.deferred, s)
				continue
			}
			res.failed = append(res.failed, fmt.Sprintf("%s/%s: pull: %v", s.Type, s.ID, err))
			continue
		}
		dest := services.FilenameFor(configDir, cid, s.Type, s.ID)
		if err := services.Save(dest, pulled); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("%s/%s: save: %v", s.Type, s.ID, err))
			continue
		}
		fmt.Printf("Wrote service yaml: %s\n", dest)
		res.written = append(res.written, dest)
	}
	return res
}

// retryDeferredServiceYAMLs re-runs the writer for services that 404'd
// on the first attempt. Called after the deploy job completes
// successfully so the conductor's async service-create has had time to
// produce a queryable resource. Errors here surface as best-effort
// warnings: a still-failing yaml just means the user runs
// `runos apps pull --force` later.
func retryDeferredServiceYAMLs(cfg *config.Config, configDir, cid string, deferred []deploy.ProvisionedService) {
	if len(deferred) == 0 {
		return
	}
	m, err := loadLocalManifest(cfg.GetAPIURL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: write service yamls: load manifest: %v\n", err)
		return
	}
	exec := dynacmd.NewExecutor(cfg.GetAPIURL())
	repoRoot, _ := git.RepoRoot()
	var stillMissing []string
	for _, s := range deferred {
		if existing := services.ExistingServiceYamlPath(repoRoot, configDir, cid, s.ID); existing != "" {
			continue
		}
		pulled, err := services.Pull(exec, m, s.Type, cid, cfg.AccountID, s.ID)
		if err != nil {
			stillMissing = append(stillMissing, fmt.Sprintf("%s/%s", s.Type, s.ID))
			continue
		}
		dest := services.FilenameFor(configDir, cid, s.Type, s.ID)
		if err := services.Save(dest, pulled); err != nil {
			stillMissing = append(stillMissing, fmt.Sprintf("%s/%s: save: %v", s.Type, s.ID, err))
			continue
		}
		fmt.Printf("Wrote service yaml: %s\n", dest)
	}
	if len(stillMissing) > 0 {
		fmt.Fprintf(os.Stderr, "Note: service yaml(s) not yet pullable for: %s. Run `runos apps pull --force` once the services finish provisioning.\n",
			strings.Join(stillMissing, ", "))
	}
}

// isAPINotFound reports whether an error chain carries a 404 from
// either the dynacmd executor or the deploy service's typed APIError
// sentinel. Used to recognise the first-deploy ordering case (the
// AppDocument hasn't been minted yet, so env-vars/dependencies/show
// 404s are correct, not bug signal) and silence the misleading
// warnings that would otherwise look like a deploy failure (I3-D).
func isAPINotFound(err error) bool {
	var dynaErr *dynacmd.APIError
	if errors.As(err, &dynaErr) {
		return dynaErr.StatusCode == http.StatusNotFound
	}
	var deployErr *deploy.APIError
	if errors.As(err, &deployErr) {
		return deployErr.StatusCode == http.StatusNotFound
	}
	return false
}

// syncResult holds what changed during a sync operation
type syncResult struct {
	deps          []deploy.AppDependency
	secretEnvVars map[string]string
	envVars       map[string]string
	// secretLocalMissingServerHas / envLocalMissingServerHas are the lists of
	// env-var names the server has stored that the local file did NOT have
	// at the moment syncAppState read it. The merge step re-introduces
	// these into the local file (deploy's env handling is additive), so a
	// user who deleted them from the file and re-deployed will silently
	// see the values come back. Surfaced to the caller so a post-deploy
	// warning can tell the user to use `runos apps sync` (replace-all) for
	// actual deletions.
	secretLocalMissingServerHas []string
	envLocalMissingServerHas    []string
}

// syncAppState syncs dependencies and env vars from the deployed app state.
// It updates the config and env file in place. Returns a result for summary printing.
func syncAppState(svc *deploy.Service, deployConfig *deploy.DeployConfig, configPath, cid string) (*syncResult, error) {
	if deployConfig.ID == "" {
		return nil, nil
	}

	configDir := filepath.Dir(configPath)
	result := &syncResult{}

	// Fetch and sync dependencies. 404 is the first-deploy ordering case
	// (the AppDocument hasn't been minted yet); the upcoming
	// PrepareDeployment will create it, so the empty result is correct
	// and the warning was just noise (I3-D).
	deps, err := svc.GetAppDependencies(deployConfig.ID)
	if err != nil {
		if !isAPINotFound(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch dependencies: %v\n", err)
		}
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

	// Fetch and sync env vars from both sources. Each side merges
	// independently with its corresponding local file so the merge is
	// surgical: a key in the secret Secret stays in .runos.{cid}.{id}.env
	// and a key in the plain ConfigMap stays in
	// runos.{cid}.{id}.config.env.
	envPaths, _ := deploy.ResolveEnvFiles(configDir, deployConfig, cid)

	// Secret side. Same 404-is-fine-on-first-deploy treatment as the
	// dependencies fetch above (I3-D).
	if secretEnvVars, err := svc.GetAppSecretEnvVars(deployConfig.ID); err != nil {
		if !isAPINotFound(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch secret env vars: %v\n", err)
		}
	} else {
		result.secretEnvVars = secretEnvVars
		if len(secretEnvVars) > 0 {
			path := envPaths.Secret
			if path == "" {
				deployConfig.SecretEnv = deploy.DefaultSecretEnvFilename(cid, deployConfig.ID)
				path = filepath.Join(configDir, deployConfig.SecretEnv)
			}
			missing, mergeErr := mergeServerEnvIntoLocalFile(path, secretEnvVars, deployConfig.SecretEnv, "secret env vars")
			if mergeErr != nil {
				return result, mergeErr
			}
			result.secretLocalMissingServerHas = missing
		}
	}

	// Plain side. Same 404 suppression (I3-D).
	if envVars, err := svc.GetAppEnvVars(deployConfig.ID); err != nil {
		if !isAPINotFound(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch env vars: %v\n", err)
		}
	} else {
		result.envVars = envVars
		if len(envVars) > 0 {
			path := envPaths.Plain
			if path == "" {
				deployConfig.Env = deploy.DefaultEnvFilename(cid, deployConfig.ID)
				path = filepath.Join(configDir, deployConfig.Env)
			}
			missing, mergeErr := mergeServerEnvIntoLocalFile(path, envVars, deployConfig.Env, "env vars")
			if mergeErr != nil {
				return result, mergeErr
			}
			result.envLocalMissingServerHas = missing
		}
	}

	// Save config
	if err := deploy.SaveConfig(configPath, deployConfig); err != nil {
		return result, fmt.Errorf("failed to save config: %w", err)
	}

	return result, nil
}

// stampSynthesizedResources fills in the resource class + cpu/memory
// fields on deployConfig when the user has nothing set locally and the
// server has populated values (the resolveRRC synthesis path), then
// rewrites the local yaml so the manifest is self-describing.
//
// Never overwrites user-set values: the local yaml stays the source of
// truth for anything the user explicitly typed. Best-effort: any I/O
// failure just leaves the local yaml absent of these fields, which is
// the pre-fix status quo. Errors warn rather than propagate.
//
// Called only after the deploy is observed successful (--follow mode)
// or right after prepare/upload in fire-and-forget mode. The pre-deploy
// syncAppState path deliberately does NOT call this so a user who
// omits RRC on purpose has the prepare endpoint reapply that omission
// rather than silently round-tripping a synthesized value.
func stampSynthesizedResources(svc *deploy.Service, c *deploy.DeployConfig, configPath string) {
	if c == nil || c.ID == "" {
		return
	}
	hasAny := c.ResourceRequirementClassID != "" ||
		c.CPURequestMc > 0 || c.CPULimitMc > 0 ||
		c.MemoryRequestMb > 0 || c.MemoryLimitMb > 0
	if hasAny {
		return
	}
	app, err := svc.GetApp(c.ID)
	if err != nil {
		// First-deploy fire-and-forget: the AppDocument hasn't
		// settled yet and a 404 here just means the synthesis hasn't
		// run. The user will see the synthesized class on their next
		// `apps_pull` once the orchestration finishes (I3-D).
		if !isAPINotFound(err) {
			fmt.Fprintf(os.Stderr, "Warning: failed to fetch synthesized resource class: %v\n", err)
		}
		return
	}
	if app == nil || app.ResourceRequirementClassID == "" {
		return
	}
	c.ResourceRequirementClassID = app.ResourceRequirementClassID
	if app.CPURequestMc > 0 {
		c.CPURequestMc = app.CPURequestMc
	}
	if app.CPULimitMc > 0 {
		c.CPULimitMc = app.CPULimitMc
	}
	if app.MemoryRequestMb > 0 {
		c.MemoryRequestMb = app.MemoryRequestMb
	}
	if app.MemoryLimitMb > 0 {
		c.MemoryLimitMb = app.MemoryLimitMb
	}
	if err := deploy.SaveConfig(configPath, c); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record synthesized resource class to local yaml: %v\n", err)
		return
	}
	fmt.Printf("Recorded synthesized resourceRequirementClassId=%q in %s\n",
		app.ResourceRequirementClassID, configPath)
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
func preDeployCodeDriftCheck(cfg *config.Config, token, cid, configPath string, force, jsonOutput bool) error {
	// Issue 86: under --json the drift refusal report must not pollute
	// stdout (CI consumers pipe stdout into jq). Redirect os.Stdout to
	// os.Stderr for the duration so every existing fmt.Print* call here
	// lands on stderr; the JSON error envelope still goes to stdout via
	// the runDeploy defer'd emitJSONError path.
	if jsonOutput {
		origStdout := os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = origStdout }()
	}
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
		// The recorded source version isn't in the server's archive
		// list. This is the easiest way to bypass the drift gate (hand
		// edit `.source-version` to garbage and the gate goes silent),
		// so refuse the deploy and require explicit `--force` consent.
		// Pre-fix: silently skipped, which made the gate trivially
		// circumventable.
		if force {
			fmt.Fprintf(os.Stderr, "Warning: recorded source version %s isn't in the server's archive list; --force passed, deploying anyway.\n", status.Recorded)
			return nil
		}
		fmt.Printf("\n%s: recorded source version %s isn't in the server's archive list (purged or never persisted).\n", localApp.App, status.Recorded)
		fmt.Println("This usually means the local .source-version file was hand-edited or the archive was deleted server-side.")
		fmt.Printf("Inspect:       runos apps list-previous-uploads %s\n", configPath)
		fmt.Printf("Reconcile:     runos apps pull %s --code --force\n", configPath)
		fmt.Printf("Deploy anyway: runos deploy --force\n")
		return fmt.Errorf("recorded source version not on server; pass --force to deploy local source as a new archive")
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
func preDeployDriftCheck(cfg *config.Config, token, cid, configPath string, force, hasLegacy, jsonOutput bool) error {
	// Issue 86: under --json, redirect os.Stdout to os.Stderr so the
	// drift refusal report (header + reconcile hints + printDiffReport)
	// doesn't pollute the JSON stdout contract. The JSON error envelope
	// still emits to stdout via runDeploy's defer'd emitJSONError.
	if jsonOutput {
		origStdout := os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = origStdout }()
	}
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
		// I14-B: a 404 from the drift gate's `GET /apps/:id` means the
		// app id in the local yaml no longer exists server-side (most
		// commonly: user deleted the app via console / `apps delete`
		// and then came back to a stale `runos.yaml`). Pre-fix the
		// gate emitted a generic "Warning: drift check failed" line
		// and proceeded with the deploy, which then failed at the
		// prepare-cli-deployment step with a different 400 — the user
		// hit a dead end with no recovery guidance. Surface the cause
		// + the two clean paths so a fresh-start deploy isn't a maze.
		var apiErr *apps.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "Error: app %q in %q no longer exists on cluster %q (server returned 404).\n", localApp.ID, configPath, cid)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Two recovery paths:")
			fmt.Fprintf(os.Stderr, "  1. Re-create as a fresh app: clear the `id:` line from %s and re-run `runos deploy`.\n", configPath)
			fmt.Fprintln(os.Stderr, "     The conductor mints a new app + osid; subsequent deploys pin to it.")
			fmt.Fprintf(os.Stderr, "  2. Bypass the gate: `runos deploy %s --force` (only useful if you genuinely want the\n", configPath)
			fmt.Fprintln(os.Stderr, "     prepare step to surface its own 404; doesn't re-create the app).")
			return fmt.Errorf("app deleted server-side; clear `id:` from yaml or pass --force")
		}
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy drift check failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "Proceeding with deploy. Run 'runos apps diff %s' manually to verify.\n", configPath)
		return nil
	}
	// I4-B: deploy gate output flows into CI logs by default; redact
	// sensitive content (secret-env values, secret-file diffs) before
	// any printDiffReport call so credentials don't end up in build
	// pipelines. The user already authored these locally — the gate
	// is informational, not diagnostic. `apps diff` keeps its
	// `--redact-secrets` opt-in for users who explicitly want the
	// values rendered for inspection.
	report.RedactSecrets()
	// emitDeletionWarning surfaces server-only fields that a deploy
	// might clear. The warning is split into two buckets:
	//   - clearOnOmit: fields the server WILL wipe because they have
	//     omit-equals-clear semantics on the PATCH endpoint
	//     (apps.OmitClearFields).
	//   - preserveOnOmit: server-only fields that stay put on push.
	// Earlier versions of this warning hardcoded "healthCheck* and
	// metrics* fields will be CLEARED" regardless of which fields were
	// actually in scope, which mismatched the bulleted list whenever
	// only preserve-on-omit fields drifted (e.g. cpu*/memory*). We now
	// only print the clearing line when there's actually something to
	// clear, and we stay quiet entirely when nothing is server-only.
	emitDeletionWarning := func() {
		if len(report.YAML.ServerOnlyFields) == 0 {
			return
		}
		// I4-F: drop fields the deploy orchestration removes via a
		// step OTHER than the apps PATCH (currently `requires.*` —
		// `replaceDependencies` handles those edges, and as of
		// conductor R2 the orphan secret keys are stripped too). The
		// pre-fix message landed `requires.<alias> (3 fields)` under
		// "Preserved server-side (no action needed)" even though the
		// user had intentionally removed the alias and the post-
		// deploy state confirmed the removal. Filtering early keeps
		// the bulleted summary honest about what's actually still in
		// scope of the apps PATCH's omit-clear / omit-preserve rules.
		serverOnly := apps.FilterOrchestrationRemoved(report.YAML.ServerOnlyFields)
		if len(serverOnly) == 0 {
			return
		}
		clearOnOmit, preserveOnOmit := apps.PartitionServerOnlyByClearSemantics(serverOnly)
		if len(clearOnOmit) == 0 && len(preserveOnOmit) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "Note: the server has fields your local yaml doesn't.")
		if len(clearOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  WILL be cleared by this deploy (omit-equals-clear):")
			for _, f := range clearOnOmit {
				fmt.Fprintf(os.Stderr, "    - %s\n", f)
			}
		}
		if len(preserveOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  Preserved server-side (no action needed):")
			for _, f := range preserveOnOmit {
				fmt.Fprintf(os.Stderr, "    - %s\n", f)
			}
		}
		if len(clearOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  To keep the cleared fields, cancel and run:")
			fmt.Fprintf(os.Stderr, "    runos apps pull %s --force\n", configPath)
			fmt.Fprintln(os.Stderr, "  which merges server state into your local yaml first.")
		}
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
		fmt.Fprintln(os.Stderr, deployDriftHeadlineForce(report))
		fmt.Fprintln(os.Stderr, "         Deploy will reconcile the server to match the local yaml.")
		if hasLegacy {
			fmt.Fprintln(os.Stderr, "         Note: this yaml uses top-level shorthand fields (port:/standardHttps:)")
			fmt.Fprintln(os.Stderr, "         that duplicate servicePortMappings[]. Forcing through means the same")
			fmt.Fprintln(os.Stderr, "         drift will reappear on every deploy.")
			fmt.Fprintf(os.Stderr, "         Recommended fix: runos apps pull %s --force\n", configPath)
		}
		fmt.Fprintln(os.Stderr)
		printDiffReport(report)
		fmt.Fprintln(os.Stderr)
		return nil
	}

	fmt.Printf("\n%s (%s) on cluster %s, %s\n", report.AppName, report.AppID, report.CID, deployDriftHeadline(report))
	fmt.Println("Deploying now would overwrite changes that aren't in your local files.")
	printDiffReport(report)
	fmt.Println()
	if hasLegacy {
		fmt.Println("Your runos.yaml uses top-level shorthand fields (`port:`, `standardHttps:`) that")
		fmt.Println("duplicate `servicePortMappings[]`. The server stores the canonical shape, which")
		fmt.Println("is the most likely cause of the drift above.")
		fmt.Println()
		fmt.Println("RECOMMENDED, migrate the local yaml to the canonical format:")
		fmt.Printf("  runos apps pull %s --force\n", configPath)
		fmt.Println("Then re-run `runos deploy`. The migration is one-time per yaml.")
		fmt.Println()
		fmt.Println("Other options:")
		fmt.Printf("  Inspect:       runos apps diff %s\n", configPath)
		fmt.Printf("  Deploy anyway: runos deploy --force   (keeps the shorthand; same drift\n")
		fmt.Println("                                        will reappear next deploy)")
	} else {
		fmt.Printf("Reconcile:  runos apps pull %s --force      (merge server state into your yaml first)\n", configPath)
		fmt.Printf("Inspect:    runos apps diff %s\n", configPath)
		fmt.Printf("Deploy anyway: runos deploy --force         (push your yaml; server state updates to match)\n")
	}
	return fmt.Errorf("upstream drift detected; pass --force to deploy anyway")
}

// deployDriftHeadline picks a directionally-correct headline for the
// deploy drift-gate refusal. Pre-fix the gate always said "the server
// has state your local yaml doesn't reflect", which was backwards or
// at best partial when the user had local additions ahead of the
// server (or both sides had unique state). Now:
//
//   - Pure server-additions (local⊆server, the most common case where
//     server-applied defaults landed after the last pull): clobber
//     warning unchanged.
//   - Mixed (both sides have unique state, e.g. local edited a value
//     the server also has but with a different value): "your yaml has
//     diverged from the server".
//
// Local-only additions (local⊇server) aren't blocking per
// NeedsForceToDeploy, so this helper is only reached for the two
// blocking cases.
//
// Regression target: I10-A.
func deployDriftHeadline(r *apps.DiffReport) string {
	yamlDirection := classifyDriftDirection(r.YAML)
	codeStale := r.Code.IsStale()
	switch {
	case yamlDirection == "server-additions" && !codeStale:
		return "the server has state your local yaml doesn't reflect."
	case yamlDirection == "mixed":
		return "your local yaml has diverged from the server."
	case codeStale && yamlDirection == "":
		return "newer source archives exist on the server than your recorded baseline."
	default:
		return "your local yaml has diverged from the server."
	}
}

// deployDriftHeadlineForce is the --force-pass-through variant of
// deployDriftHeadline. Same direction logic; phrased as a warning
// rather than a refusal headline.
func deployDriftHeadlineForce(r *apps.DiffReport) string {
	yamlDirection := classifyDriftDirection(r.YAML)
	switch yamlDirection {
	case "server-additions":
		return "Warning: server has changes your local yaml doesn't reflect, but --force was passed."
	case "mixed":
		return "Warning: your local yaml has diverged from the server, but --force was passed."
	default:
		return "Warning: drift detected, but --force was passed."
	}
}

// classifyDriftDirection inspects a SectionDiff's AdditiveOnly +
// LocalIsSuperset flags and returns "server-additions" (local⊆server),
// "local-additions" (server⊆local), "mixed" (neither subset), or ""
// (no drift). Used by the drift-gate headline so the user sees which
// side has the unique state.
func classifyDriftDirection(sd apps.SectionDiff) string {
	if sd.Status != apps.StatusDrift {
		return ""
	}
	switch {
	case sd.AdditiveOnly && !sd.LocalIsSuperset:
		return "server-additions"
	case !sd.AdditiveOnly && sd.LocalIsSuperset:
		return "local-additions"
	default:
		return "mixed"
	}
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

	// Cross-check yaml's cid against flag/config cid before overwriting
	// deployConfig.CID below. Same I18-B fix as runDeploy: refuse on
	// mismatch instead of silently overwriting.
	cid, err = deploy.ReconcileCID(cid, deployConfig.CID)
	if err != nil {
		cmd.SilenceUsage = true
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

// deployRequiresEnvCollisions adapts deploy.DeployConfig.Requires (a
// map keyed by alias of deploy.ServiceRequirement) into the
// apps.ServiceRequirement shape FindServerInjectedEnvCollisions
// accepts, then runs the check. The two ServiceRequirement types
// have the same wire shape but live in different packages; this
// keeps deploy from importing apps internals beyond the helper.
func deployRequiresEnvCollisions(cfg *deploy.DeployConfig, localEnv map[string]string) []apps.ServerInjectedEnvCollision {
	if len(cfg.Requires) == 0 || len(localEnv) == 0 {
		return nil
	}
	adapted := make(map[string]apps.ServiceRequirement, len(cfg.Requires))
	for alias, r := range cfg.Requires {
		adapted[alias] = apps.ServiceRequirement{Env: r.Env}
	}
	return apps.FindServerInjectedEnvCollisions(localEnv, adapted)
}

// mergeServerEnvIntoLocalFile reconciles one server-side env-var map with
// its corresponding local file. The merge is local-wins: server-only keys
// are pulled DOWN into the local file (so a teammate's console-set env
// shows up locally on the next deploy), but any key the local file already
// has — whether the value matches the server or not — is left untouched.
// The deploy that follows then pushes the local file up, which replaces
// the server-side env in full and resolves any divergence in the user's
// favour without us having to gate or warn.
//
// Returns the sorted list of keys the server has that the local file lacked
// at read time. Used by the caller's post-deploy `warnLocalDeletions` for
// the case-E hint: the user *deleted* a key locally, this pull silently
// re-added it, and the deploy then re-pushed it. Re-introducing a deleted
// key is the only genuinely surprising direction; deploy-time edits to
// existing keys (the dominant path — bumping a version, flipping a flag)
// are not surfaced because they're exactly what the user expected.
//
// Intentionally per-side because the secret/plain split runs the same
// logic against different files.
//
// Error return is reserved for I/O failures (file write errors). Mismatched
// local/server values are not errors and never abort the caller.
func mergeServerEnvIntoLocalFile(
	path string,
	serverVars map[string]string,
	_ string, // relPathForErrors: kept for caller signature stability; merge never errors on conflict now
	label string,
) ([]string, error) {
	localVars, err := deploy.LoadEnvFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to read existing %s file: %v\n", label, err)
	}

	// Local-missing tracking: keys the merge below is about to re-introduce.
	// Only meaningful when the local file actually existed beforehand — a
	// first-time materialisation is a legitimate fresh pull, not a
	// deletion-that-got-reverted.
	var missing []string
	if _, statErr := os.Stat(path); statErr == nil {
		for key := range serverVars {
			if _, present := localVars[key]; !present {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
	}

	// Local-wins merge: copy local first so its values can never be
	// clobbered, then add only the server-only keys. This makes the
	// "same-key-different-value" case (the dominant path on every version
	// bump) a non-event: the local file is preserved verbatim and the
	// deploy that follows pushes those values up.
	merged := make(map[string]string, len(serverVars)+len(localVars))
	for k, v := range localVars {
		merged[k] = v
	}
	hasServerOnlyKeys := false
	for k, v := range serverVars {
		if _, exists := merged[k]; !exists {
			merged[k] = v
			hasServerOnlyKeys = true
		}
	}

	// Skip the file write only when local already has every server key,
	// i.e. the merge would be a strict no-op. We can't reuse `len(missing)`
	// here because that's intentionally empty when the local file doesn't
	// exist (defensive: first-time materialisation isn't a "user deleted
	// these"). Skipping in that case would leave the local file absent on
	// fresh-checkout deploys, the subsequent LoadEnvFile would return nil,
	// and customEnvVars would be omitted from the deploy body — which
	// conductor would interpret as "user wants empty ConfigMap" and wipe
	// the server's env on every deploy from a fresh clone.
	if !hasServerOnlyKeys {
		return missing, nil
	}

	if err := deploy.SaveEnvFile(path, merged); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save %s file: %v\n", label, err)
	}
	return missing, nil
}

// warnLocalDeletions prints the post-deploy "got merged back" hint for one
// env-file side. `path` is the file the user edits, `missing` is the list
// of keys present on the server but absent from the local file at the
// moment the pre-deploy merge ran. `requiresFiltered` (optional) filters
// out keys already surfaced by the requires-env-shadowing warning so we
// don't double-warn for the same key with two different framings.
func warnLocalDeletions(path string, missing []string, requiresFiltered map[string]bool) {
	if path == "" || len(missing) == 0 {
		return
	}
	var filtered []string
	for _, k := range missing {
		if requiresFiltered != nil && requiresFiltered[k] {
			continue
		}
		filtered = append(filtered, k)
	}
	if len(filtered) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr,
		"Warning: %d env key(s) on the server were missing from %s and got merged back into the local file:\n",
		len(filtered), path)
	for _, k := range filtered {
		fmt.Fprintf(os.Stderr, "  %s\n", k)
	}
	fmt.Fprintf(os.Stderr, "\n`runos deploy` is additive: it pulls server env vars down into local but never pushes deletions up.\n")
	fmt.Fprintf(os.Stderr, "If you intended to remove these from the server, run `runos apps sync` (NOT another deploy);\n")
	fmt.Fprintf(os.Stderr, "the replace-all env-vars push is the only way the CLI deletes server-side env vars.\n")
}

// preDeployDomainRemovalGate fetches the per-app custom-domain list,
// diffs against the local yaml's declared fqdns, and surfaces a
// destructive-removal warning when any server-side domain is about to
// be removed by this deploy.
//
// I2-4e regression target (TEST_LOG.md): conductor's reconciler treats
// omit as delete (consistent with omit-equals-clear elsewhere), so
// dropping a `domain:` line or a `servicePortMappings[].domains[]`
// entry silently removes the mapping AND its DNS record on next
// deploy. The previous CLI gave no warning.
//
// I2-4e' refinement: the WARNING block ALWAYS prints when a removal is
// detected, regardless of `--force` / `--yes` / non-tty. Only the
// interactive y/N prompt is conditional. Earlier the gate auto-
// skipped entirely under `--force`, but the upstream drift gate
// already funnels users to `--force` for any local-vs-server
// divergence, so the warning never surfaced in practice. Now the
// destructive-action signal is in the build log no matter how the
// deploy was invoked.
//
// Skip semantics for the prompt:
//   - --force: warning prints, proceeds without prompt
//   - --yes: warning prints, proceeds without prompt
//   - stdin not a TTY (CI, piped input): warning prints, proceeds
//   - otherwise: warning prints, prompts y/N, anything other than
//     y/yes aborts
//
// Best-effort fetch: a failure to call GetAppCustomDomains warns and
// proceeds. Refusing the deploy because a read endpoint hiccupped
// would be worse than the prior status quo (no warning at all).
//
// Returns nil to proceed; a non-nil error aborts the deploy (only
// possible when the interactive prompt is reached and the user answers
// no).
func preDeployDomainRemovalGate(svc *deploy.Service, c *deploy.DeployConfig, force, skipPrompt bool) error {
	if c == nil || c.ID == "" {
		return nil
	}
	serverDomains, err := svc.GetAppCustomDomains(c.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: domain-removal gate skipped (fetch failed: %v)\n", err)
		return nil
	}
	if len(serverDomains) == 0 {
		return nil
	}
	local := make(map[string]struct{})
	for _, fqdn := range deploy.LocalDomainFqdns(c) {
		local[fqdn] = struct{}{}
	}
	var removals []string
	for _, fqdn := range serverDomains {
		if _, kept := local[fqdn]; !kept {
			removals = append(removals, fqdn)
		}
	}
	if len(removals) == 0 {
		return nil
	}

	// Always surface the warning. Skipping the prompt does not skip
	// the destructive-action signal (I2-4e' fix).
	fmt.Fprintln(os.Stderr, "WARNING: this deploy will REMOVE the following custom domain(s) attached to the app:")
	for _, fqdn := range removals {
		fmt.Fprintf(os.Stderr, "  - %s\n", fqdn)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Removal also drops any provider-managed DNS record (Cloudflare, etc.).")
	fmt.Fprintln(os.Stderr, "To keep these domains, cancel and add them back to runos.yaml's `domain:` or")
	fmt.Fprintln(os.Stderr, "`servicePortMappings[].domains[]` before redeploying.")

	if force || skipPrompt {
		fmt.Fprintln(os.Stderr)
		switch {
		case force:
			fmt.Fprintln(os.Stderr, "--force passed; proceeding without prompt.")
		case skipPrompt:
			fmt.Fprintln(os.Stderr, "--yes passed; proceeding without prompt.")
		}
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "stdin is not a terminal (CI / piped input); proceeding without prompt.")
		return nil
	}
	ok, err := confirm("\nProceed with removal? [y/N] ")
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !ok {
		return fmt.Errorf("deploy cancelled to preserve custom domain(s)")
	}
	return nil
}

// confirmDeploy prints summary on stderr and prompts the user to confirm.
// Auto-skips when stdin is not a terminal (CI, piped input) or when
// skipPrompt is true (--yes). Returns a non-nil error to abort the deploy
// when the user answers anything other than y/yes.
//
// The non-TTY auto-skip is intentional: CI runners almost always run with
// stdin closed or redirected to /dev/null, and refusing those runs would
// make the prompt useless in the only place it can't catch a typo (because
// there's no human watching). --yes remains the explicit opt-out for TTY
// users who want to bypass the prompt.
func confirmDeploy(summary string, skipPrompt bool) error {
	if skipPrompt {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	fmt.Fprint(os.Stderr, summary)
	ok, err := confirm("\nProceed with deploy? [y/N] ")
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !ok {
		return fmt.Errorf("deploy cancelled")
	}
	return nil
}

// buildCLIDeploySummary returns the summary block shown before a CLI
// deploy. Lists the app, cluster, account, and resolved build context
// path so the user can spot wrong-directory / wrong-cluster mistakes
// before any tarball or upload work begins.
func buildCLIDeploySummary(appName, cid, aid, archiveRoot string) string {
	return fmt.Sprintf(`Deploy plan (CLI deploy):
  App:      %s
  Cluster:  %s
  Account:  %s
  Source:   %s
`, appName, cid, aid, archiveRoot)
}

// buildVCSDeploySummary returns the summary block shown before a VCS
// deploy. configPath may be empty when the CLI couldn't auto-derive it
// (CI mode without a checkout, or yaml outside the repo); the message
// notes that the server falls back to whatever the AppDocument has stored.
func buildVCSDeploySummary(appID, cid, aid, sha, configPath string) string {
	cp := configPath
	if cp == "" {
		cp = "<server default>"
	}
	return fmt.Sprintf(`Deploy plan (VCS deploy):
  App:        %s
  Cluster:    %s
  Account:    %s
  SHA:        %s
  configPath: %s
`, appID, cid, aid, shortSHA(sha), cp)
}

// envKeyConflicts returns the sorted intersection of two env-var maps.
// A non-empty result means the user has the same key in both their
// secret and plain env files, which the conductor refuses to apply
// (envFrom merging would make the running pod's value ambiguous).
func envKeyConflicts(secret, plain map[string]string) []string {
	if len(secret) == 0 || len(plain) == 0 {
		return nil
	}
	var conflicts []string
	for k := range plain {
		if _, ok := secret[k]; ok {
			conflicts = append(conflicts, k)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}
