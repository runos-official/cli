package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/buildargs"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/git"
	"github.com/runos-official/cli/internal/jobs"

	"github.com/spf13/cobra"
)

var appsBuildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build and push a VCS app's image to Harbor (no rollout, no command)",
	Long: `Build and push the SHA-keyed image for a VCS app to Harbor and stop.
No rollout, no command run. Sibling to runos deploy / runos run: build is
just the build step surfaced as its own verb so a CI pipeline can read as
three single-purpose steps with attributable output.

Two invocation shapes are supported:

  CI shape (no runos.yaml on disk):
    runos apps build --app <id> --sha <sha> --cid <cid> [--build-arg KEY=VAL]...

  Laptop shape (runos.yaml in cwd):
    runos apps build [--build-arg KEY=VAL]...

In the laptop shape the app id is loaded from runos.yaml, --sha defaults
to git HEAD (a dirty working tree is refused unless --allow-dirty is
set), and --cid resolves via the same three-source order as runos deploy
(flag, then CLI config default, then the yaml's cid: field).

The verb follows the CLI's job convention: it returns immediately after
queueing by default. Pass --follow to stream build progress and block
until the build finishes, propagating a non-zero exit on build failure
so a CI step gates on the build outcome.

--build-arg mirrors runos deploy: repeated KEY=VAL flags are merged with
the yaml buildArgs: map server-side (CLI > yaml). Same SHA + same build
args produces the byte-identical image a subsequent runos deploy / run
would produce, so the image is reused from Harbor with no rebuild.

Only VCS-deploy apps are supported; CLI-deploy (tarball) apps are
refused with a clear error.

Examples:
  runos apps build --app appid7 --sha $GITHUB_SHA --cid mycluster --follow
  runos apps build --follow
  runos apps build --app appid7 --sha $SHA --cid mycluster --build-arg PYTHON_VERSION=3.12 --follow`,
	SilenceUsage: true,
	RunE:         runAppsBuild,
}

func init() {
	appsBuildCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file (laptop shape)")
	appsBuildCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	appsBuildCmd.Flags().BoolP("follow", "f", false, "follow build progress until completion; without it, exits 0 the moment the conductor accepts the request")
	appsBuildCmd.Flags().BoolP("json", "j", false, "emit a JSON envelope on stdout (jobId, appId, sha, configPath, and imageTag/skippedBecauseCached/durationMs when --follow reaches a terminal state); humans on stderr")
	appsBuildCmd.Flags().String("app", "", "app ID to build (CI shape; no runos.yaml needed)")
	appsBuildCmd.Flags().String("sha", "", "commit SHA to build at (defaults to git HEAD in laptop shape)")
	appsBuildCmd.Flags().Bool("allow-dirty", false, "build at HEAD even when the working tree has uncommitted changes (laptop shape only)")
	appsBuildCmd.Flags().BoolP("yes", "y", false, "skip the build confirmation prompt (auto-skipped when stdin is not a terminal, e.g. CI)")
	appsBuildCmd.Flags().StringArray("build-arg", nil, "Docker build arg `KEY=VALUE` (repeatable). Merged server-side with runos.yaml buildArgs:; --build-arg wins on conflicts. Duplicate keys within a single invocation are rejected.")
	appsCmd.AddCommand(appsBuildCmd)
}

func runAppsBuild(cmd *cobra.Command, args []string) (rerr error) {
	cmd.SilenceUsage = true

	jsonOutput, _ := cmd.Flags().GetBool("json")
	defer func() {
		if jsonOutput && rerr != nil {
			rerr = emitJSONError(cmd, rerr)
		}
	}()

	// Under --json, route human-readable progress to stderr so stdout
	// stays a single parseable document. Mirrors run / deploy --json.
	humanOut := io.Writer(os.Stdout)
	stdout := io.Writer(os.Stdout)
	if jsonOutput {
		humanOut = os.Stderr
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' or set RUNOS_API_KEY (%w)", err)
	}

	aid := cfg.GetAccountID()
	if aid == "" {
		return fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
	}
	cfg.AccountID = aid

	flagApp, _ := cmd.Flags().GetString("app")
	flagSha, _ := cmd.Flags().GetString("sha")
	flagCid, _ := cmd.Flags().GetString("cid")
	flagAllowDirty, _ := cmd.Flags().GetBool("allow-dirty")
	flagFollow, _ := cmd.Flags().GetBool("follow")
	flagYes, _ := cmd.Flags().GetBool("yes")

	// Parse --build-arg once, here, before any branch. Same parser as
	// runos deploy so the wire body is byte-identical at the same SHA.
	rawBuildArgs, _ := cmd.Flags().GetStringArray("build-arg")
	buildArgsCli, err := buildargs.Parse(rawBuildArgs)
	if err != nil {
		return err
	}

	cid := flagCid
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}

	var appID, sha, configPath string
	var shaProvided bool

	if flagApp != "" {
		// CI shape: --app pins the target, no yaml is consulted, cid must
		// come from flag/config (no yaml fallback available).
		if cid == "" {
			return fmt.Errorf("cluster ID required when using --app: pass --cid or set default with 'runos config set cid <cluster-id>'")
		}
		appID = flagApp
		sha = flagSha
		shaProvided = sha != ""
		if sha == "" {
			return fmt.Errorf("--sha is required when using --app (CI shape has no yaml + git fallback)")
		}
	} else {
		// Laptop shape: load runos.yaml, derive app id, default sha to
		// git HEAD (dirty-tree refusal unless --allow-dirty), and apply
		// deploy's three-source cid resolution.
		yamlConfigPath, _ := cmd.Flags().GetString("config")
		if !filepath.IsAbs(yamlConfigPath) {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}
			yamlConfigPath = filepath.Join(cwd, yamlConfigPath)
		}
		deployConfig, loadErr := deploy.LoadConfig(yamlConfigPath)
		if loadErr != nil {
			if !cmd.Flags().Changed("config") {
				detected, detectErr := autoDetectDeployYAML(filepath.Dir(yamlConfigPath))
				if detectErr != nil {
					return detectErr
				}
				if detected != "" {
					yamlConfigPath = detected
					deployConfig, loadErr = deploy.LoadConfig(yamlConfigPath)
				}
			}
			if loadErr != nil {
				return fmt.Errorf("%w (or pass --app for the CI shape)", loadErr)
			}
		}
		if deployConfig.ID == "" {
			return fmt.Errorf("runos.yaml is missing `id:`; run `runos apps pull --id <id>` first, or use the CI shape with --app")
		}
		appID = deployConfig.ID

		cid, err = deploy.ReconcileCID(cid, deployConfig.CID)
		if err != nil {
			return err
		}
		if cid == "" {
			return fmt.Errorf("cluster ID required: pass --cid, run 'runos config set cid <cluster-id>', or include cid: in %s", yamlConfigPath)
		}

		configPath = resolveVcsConfigPath(deployConfig, yamlConfigPath)

		sha = flagSha
		shaProvided = sha != ""
		if sha == "" {
			if !git.IsRepo() {
				return fmt.Errorf("--sha is required when not running from inside a git checkout")
			}
			head, headErr := git.GetHEAD()
			if headErr != nil {
				return fmt.Errorf("could not resolve HEAD: %w", headErr)
			}
			sha = head
		}
	}

	sha = strings.ToLower(sha)
	if err := validateCommitSHA(sha); err != nil {
		return err
	}

	// Dirty-tree gate: only meaningful when sha was auto-derived from HEAD
	// (laptop shape, no --sha). Explicit --sha is orthogonal to the local
	// tree state since the build runs against the committed source at <sha>.
	if flagApp == "" && !shaProvided && git.IsRepo() {
		dirty, derr := git.IsDirty()
		if derr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check git status: %v\n", derr)
		} else if dirty {
			if !flagAllowDirty {
				return fmt.Errorf("working tree is dirty; commit changes or pass --allow-dirty to build at HEAD anyway")
			}
			fmt.Fprintln(os.Stderr, "Warning: building with --allow-dirty; uncommitted local changes will NOT be in the image")
		}
	}

	svc := deploy.NewService(cfg.GetAPIURL(), token, cid, aid)

	// VCS-only preflight, mirroring runos run: refuse non-VCS apps before
	// any conductor round-trip so the error surfaces close to argv.
	app, err := svc.GetApp(appID)
	if err != nil {
		var apiErr *deploy.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("app %s not found on cluster %s", appID, cid)
		}
		return fmt.Errorf("failed to look up app %s: %w", appID, err)
	}
	if app == nil {
		return fmt.Errorf("app %s not found on cluster %s", appID, cid)
	}
	if err := enforceBuildVCSDeployType(app.DeployType); err != nil {
		return err
	}

	if err := confirmDeploy(buildAppsBuildSummary(appID, cid, aid, sha, configPath), flagYes); err != nil {
		return err
	}

	fmt.Fprintf(humanOut, "Building app %s @ %s...\n", appID, shortSHA(sha))
	if configPath != "" {
		fmt.Fprintf(humanOut, "  configPath: %s\n", configPath)
	} else {
		fmt.Fprintln(humanOut, "  configPath: (using AppDocument default)")
	}

	resp, err := svc.BuildOnly(appID, sha, configPath, buildArgsCli)
	if err != nil {
		return fmt.Errorf("failed to trigger build: %w", err)
	}

	if !flagFollow {
		fmt.Fprintf(humanOut, "\nBuild queued:\n")
		fmt.Fprintf(humanOut, "  Job ID: %s\n", resp.JobID)
		fmt.Fprintf(humanOut, "  App ID: %s\n", appID)
		fmt.Fprintf(humanOut, "  SHA:    %s\n", sha)
		fmt.Fprintf(humanOut, "\nFollow build: runos follow %s\n", resp.JobID)
		if jsonOutput {
			envelope := appsBuildJSONResponse{
				JobID: resp.JobID, AppID: appID, SHA: sha, ConfigPath: configPath,
			}
			return writeJSON(stdout, envelope)
		}
		return nil
	}

	fmt.Fprintln(humanOut, "\nFollowing build progress...")

	jobSvc, err := jobs.NewService()
	if err != nil {
		return err
	}

	// 30-minute follow ceiling, matching FollowJobToWriter's default.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	followErr := jobs.FollowJobWithServiceToWriter(ctx, jobSvc, resp.JobID, humanOut)

	final, getErr := jobSvc.GetStatus(resp.JobID)
	if getErr != nil {
		if followErr != nil {
			return followErr
		}
		return getErr
	}

	build, _ := final.BuildResult()

	envelope := appsBuildJSONResponse{
		JobID: resp.JobID, AppID: appID, SHA: sha, ConfigPath: configPath,
	}
	if build != nil {
		envelope.ImageTag = build.ImageTag
		envelope.DurationMs = build.DurationMs
		cached := build.SkippedBecauseCached
		envelope.SkippedBecauseCached = &cached
	}

	switch {
	case followErr == nil && final.Status == "completed":
		summarizeBuildResult(humanOut, build)
		if jsonOutput {
			return writeJSON(stdout, envelope)
		}
		return nil
	case final.Status == "failed":
		msg := final.Error
		if msg == "" && followErr != nil {
			msg = followErr.Error()
		}
		if !jsonOutput {
			fmt.Fprintf(humanOut, "\nBuild failed: %s\n", msg)
		}
		if jsonOutput {
			// Emit the envelope on stdout AND return the wrapped error so
			// the deferred emitJSONError doesn't double-encode. The error
			// path matches deploy --json: failure carries the same shape.
			_ = writeJSON(stdout, envelope)
		}
		return fmt.Errorf("build failed: %s", msg)
	default:
		if followErr != nil {
			return followErr
		}
		return fmt.Errorf("build ended in unexpected status %q", final.Status)
	}
}

// appsBuildJSONResponse is the --json envelope written on stdout. The
// last three fields are populated only when --follow reaches a terminal
// state and the conductor's jobs.result is present. SkippedBecauseCached
// is *bool so omitempty distinguishes "absent" (no --follow / no result
// yet) from explicit false (built fresh, not cached). foreman objective
// 43 / story 74.
type appsBuildJSONResponse struct {
	JobID                string `json:"jobId"`
	AppID                string `json:"appId"`
	SHA                  string `json:"sha"`
	ConfigPath           string `json:"configPath,omitempty"`
	ImageTag             string `json:"imageTag,omitempty"`
	SkippedBecauseCached *bool  `json:"skippedBecauseCached,omitempty"`
	DurationMs           int64  `json:"durationMs,omitempty"`
}

// enforceBuildVCSDeployType mirrors enforceVCSDeployType from cmd/run.go.
// Kept local so the two callers stay independently auditable; lifting to
// a shared file is a future refactor when a third caller appears.
func enforceBuildVCSDeployType(deployType string) error {
	if deployType == "vcs" {
		return nil
	}
	if deployType == "" {
		return fmt.Errorf("runos apps build requires a VCS-deploy app; this app has no deployType set (run `runos apps pull` to refresh)")
	}
	return fmt.Errorf("runos apps build is only valid for VCS-deployed apps; this app has deployType=%q. Use `runos deploy` for CLI-deploy apps", deployType)
}

// buildAppsBuildSummary renders the pre-dispatch confirmation block.
// configPath empty renders as "<server default>" so users can see the
// AppDocument fallback is in play.
func buildAppsBuildSummary(appID, cid, aid, sha, configPath string) string {
	cp := configPath
	if cp == "" {
		cp = "<server default>"
	}
	return fmt.Sprintf(`Build plan:
  App:        %s
  Cluster:    %s
  Account:    %s
  SHA:        %s
  configPath: %s
`, appID, cid, aid, shortSHA(sha), cp)
}

// summarizeBuildResult writes the one-line success summary appropriate
// for the final structured result. Cached path is the most common
// success case in a multi-step CI pipeline (build / run / deploy at
// the same SHA), so it gets a distinct phrasing.
func summarizeBuildResult(w io.Writer, r *jobs.BuildResult) {
	if r == nil {
		fmt.Fprintln(w, "\nBuild completed.")
		return
	}
	if r.SkippedBecauseCached {
		fmt.Fprintln(w, "\nImage already cached.")
		return
	}
	if r.ImageTag != "" {
		fmt.Fprintf(w, "\nBuilt %s in %s.\n", r.ImageTag, formatDurationMs(r.DurationMs))
		return
	}
	fmt.Fprintf(w, "\nBuild completed in %s.\n", formatDurationMs(r.DurationMs))
}

// formatDurationMs renders a wall-time millisecond count as a short
// duration string. Zero is "<1s" so the cached-path's tiny but
// truthful durationMs reads honestly.
func formatDurationMs(ms int64) string {
	if ms <= 0 {
		return "<1s"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return "<1s"
	}
	return d.Round(time.Second).String()
}
