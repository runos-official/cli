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
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/git"
	"github.com/runos-official/cli/internal/jobs"

	"github.com/spf13/cobra"
)

// runDefaultTimeoutDisplay is the human-readable default surfaced in
// --help. The conductor enforces the real default (1800s as of
// MANIFEST_VERSION 28.14.0) and a 7200s upper bound; the CLI just
// forwards a positive value or leaves the field unset to opt into the
// server default. Kept as a display constant so the help text doesn't
// silently drift if the server default changes.
const runDefaultTimeoutDisplay = "30m (server default)"

// exitCodeError lets runRun signal a specific process exit code (e.g.
// the container's real exit code, or 124 on timeout) back to the cobra
// Execute() wrapper without losing the cobra error-formatting flow.
// Implementations are unwrapped via errors.As; see Execute() in
// cmd/root.go for the dispatch.
type exitCodeError struct {
	code int
	msg  string
}

// Error returns the diagnostic message.
func (e *exitCodeError) Error() string { return e.msg }

// ExitCode returns the process exit code the CLI should terminate with.
func (e *exitCodeError) ExitCode() int { return e.code }

var runCmd = &cobra.Command{
	Use:   "run [flags] [--] <script-or-command> [args...]",
	Short: "Execute a one-off task in the cluster from a VCS app's image",
	Long: `Run a one-off task (typically a DB migration, seed, or backfill) inside
the cluster, against the exact image for a given commit SHA, using the
app's own env and secrets. Sibling to runos deploy: deploy is build then
rollout, run is build then execute.

Two invocation shapes are supported:

  CI shape (no runos.yaml on disk):
    runos run --app <id> --sha <sha> --cid <cid> [--] <script-or-command>

  Laptop shape (runos.yaml in cwd):
    runos run [--] <script-or-command>

In the laptop shape the app id is loaded from runos.yaml, --sha defaults
to git HEAD (a dirty working tree is refused unless --allow-dirty is
set), and --cid resolves via the same three-source order as runos deploy
(flag, then CLI config default, then the yaml's cid: field).

The image is built on-demand if missing for the given SHA, and reused
from Harbor if present. The container runs in the app's namespace with
the app's existing ConfigMap + Secret injected. Logs stream to stdout,
the CLI blocks until the run reaches a terminal state, and exits with
the container's real exit code so a CI step that gates on the run will
fail when the command fails.

Only VCS-deploy apps are supported; CLI-deploy (tarball) apps are
refused with a clear error.

Examples:
  runos run --app appid7 --sha $GITHUB_SHA --cid mycluster scripts/release.sh
  runos run --app appid7 --sha $GITHUB_SHA --cid mycluster -- alembic upgrade head
  runos run -- alembic upgrade head    # laptop shape; reads runos.yaml`,
	Args:         cobra.MinimumNArgs(1),
	SilenceUsage: true,
	RunE:         runRun,
}

func init() {
	runCmd.Flags().StringP("config", "c", "runos.yaml", "path to config file (laptop shape)")
	runCmd.Flags().StringP("cid", "", "", "cluster ID (overrides default)")
	runCmd.Flags().BoolP("json", "j", false, "emit a JSON envelope on stdout with audit-record id + exit code + sha; route progress + container stdout to stderr")
	runCmd.Flags().String("app", "", "app ID to run against (CI shape; no runos.yaml needed)")
	runCmd.Flags().String("sha", "", "commit SHA to run at (defaults to git HEAD in laptop shape)")
	runCmd.Flags().Bool("allow-dirty", false, "deploy at HEAD even when the working tree has uncommitted changes (laptop shape only)")
	runCmd.Flags().String("timeout", "", "kill the run after this duration, e.g. 5m, 1h. Default: "+runDefaultTimeoutDisplay)
	runCmd.Flags().BoolP("yes", "y", false, "skip the run confirmation prompt (auto-skipped when stdin is not a terminal, e.g. CI)")
}

func runRun(cmd *cobra.Command, args []string) (rerr error) {
	cmd.SilenceUsage = true

	jsonOutput, _ := cmd.Flags().GetBool("json")
	// JSON-mode error envelope mirrors the deploy contract: keep stdout
	// a single JSON document on the failure path too. Wrapped via the
	// existing emitJSONError helper so the shape matches deploy's.
	defer func() {
		if jsonOutput && rerr != nil {
			// Preserve the exit-code carrier when wrapping.
			var ec *exitCodeError
			if errors.As(rerr, &ec) {
				_ = emitJSONError(cmd, rerr)
				rerr = ec
				return
			}
			rerr = emitJSONError(cmd, rerr)
		}
	}()

	// humanOut routes the CLI's own banner lines and the streamed
	// work-item output. In non-JSON mode this is stdout (mirroring
	// `deploy --follow` so a CI step can `tee` or pipe the run output).
	// Under --json, everything except the terminal JSON envelope routes
	// to stderr so stdout stays a single parseable document for `jq`.
	humanOut := io.Writer(os.Stdout)
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

	// Args = the command + its args. cobra collects everything after the
	// verb (including everything past `--`) into args, so the user can
	// write either:
	//   runos run scripts/release.sh
	//   runos run -- alembic upgrade head
	// and we forward the slice verbatim. `--` is only ever needed to
	// disambiguate command args that look like CLI flags.
	command := append([]string(nil), args...)
	if len(command) == 0 || command[0] == "" {
		return fmt.Errorf("script or command is required")
	}

	flagApp, _ := cmd.Flags().GetString("app")
	flagSha, _ := cmd.Flags().GetString("sha")
	flagCid, _ := cmd.Flags().GetString("cid")
	flagAllowDirty, _ := cmd.Flags().GetBool("allow-dirty")
	flagYes, _ := cmd.Flags().GetBool("yes")
	flagTimeout, _ := cmd.Flags().GetString("timeout")

	timeoutSeconds, err := parseRunTimeout(flagTimeout)
	if err != nil {
		return err
	}

	cid := flagCid
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}

	var appID, sha string
	var shaProvided bool

	if flagApp != "" {
		// CI shape: --app pins the target, no yaml is consulted, cid
		// must come from flag/config (no yaml fallback available).
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
		configPath, _ := cmd.Flags().GetString("config")
		if !filepath.IsAbs(configPath) {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}
			configPath = filepath.Join(cwd, configPath)
		}
		deployConfig, loadErr := deploy.LoadConfig(configPath)
		if loadErr != nil {
			if !cmd.Flags().Changed("config") {
				detected, detectErr := autoDetectDeployYAML(filepath.Dir(configPath))
				if detectErr != nil {
					return detectErr
				}
				if detected != "" {
					configPath = detected
					deployConfig, loadErr = deploy.LoadConfig(configPath)
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
			return fmt.Errorf("cluster ID required: pass --cid, run 'runos config set cid <cluster-id>', or include cid: in %s", configPath)
		}

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

	// Normalise + validate SHA. Git accepts uppercase but the conductor's
	// SHA_REGEX is lowercase; mirror the deploy_vcs.go normalisation.
	sha = strings.ToLower(sha)
	if err := validateCommitSHA(sha); err != nil {
		return err
	}

	// Dirty-tree gate (laptop, sha auto-derived from HEAD only; explicit
	// --sha against a server-side commit is orthogonal to the local tree).
	if flagApp == "" && !shaProvided && git.IsRepo() {
		dirty, derr := git.IsDirty()
		if derr != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check git status: %v\n", derr)
		} else if dirty {
			if !flagAllowDirty {
				return fmt.Errorf("working tree is dirty; commit changes or pass --allow-dirty to run at HEAD anyway")
			}
			fmt.Fprintln(os.Stderr, "Warning: running with --allow-dirty; uncommitted local changes will NOT be in the image")
		}
	}

	svc := deploy.NewService(cfg.GetAPIURL(), token, cid, aid)

	// VCS-only preflight: hard-refuse CLI-deploy apps locally before any
	// run request hits the conductor. The conductor also enforces this
	// (returns 400), but failing on the client surfaces the error closer
	// to the user's argv with no wasted round-trip.
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
	if err := enforceVCSDeployType(app.DeployType); err != nil {
		return err
	}

	// Confirmation gate (auto-skip in CI / non-TTY / --yes), mirrors deploy.
	if err := confirmDeploy(buildRunSummary(appID, cid, aid, sha, command, timeoutSeconds), flagYes); err != nil {
		return err
	}

	fmt.Fprintf(humanOut, "Running %s @ %s in app %s (cluster %s)...\n", joinCommand(command), shortSHA(sha), appID, cid)

	resp, err := svc.Run(appID, sha, command, timeoutSeconds)
	if err != nil {
		// Surface conductor 409 (already in flight) verbatim; the body
		// names the in-flight jobId so the user can attach to it.
		var apiErr *deploy.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
			return fmt.Errorf("another run is already in progress for this app: %s", strings.TrimSpace(string(apiErr.Body)))
		}
		return fmt.Errorf("failed to trigger run: %w", err)
	}

	fmt.Fprintf(humanOut, "  Job ID: %s\n", resp.JobID)
	if resp.OSID != "" {
		fmt.Fprintf(humanOut, "  OSID:   %s\n", resp.OSID)
	}
	fmt.Fprintln(humanOut, "\nFollowing run progress...")

	jobSvc, err := jobs.NewService()
	if err != nil {
		return err
	}

	// Hard upper bound on follow wall time: timeoutSeconds + a grace
	// margin for the conductor's own dispatch + cleanup overhead. When
	// timeoutSeconds is 0 (server-default), use a 2h ceiling (matches
	// the conductor's documented hard cap).
	followBudget := 2 * time.Hour
	if timeoutSeconds > 0 {
		followBudget = time.Duration(timeoutSeconds)*time.Second + 5*time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), followBudget)
	defer cancel()

	followErr := jobs.FollowJobWithServiceToWriter(ctx, jobSvc, resp.JobID, humanOut)

	final, getErr := jobSvc.GetStatus(resp.JobID)
	if getErr != nil {
		if followErr != nil {
			return followErr
		}
		return getErr
	}

	exitCode, hasResult := extractRunExitCode(final)

	if jsonOutput {
		envelope := runJSONResponse{
			JobID: resp.JobID, OSID: resp.OSID, AppID: appID, SHA: sha,
			Status: final.Status, ExitCode: exitCode,
		}
		_ = writeJSON(os.Stdout, envelope)
	}

	switch {
	case followErr == nil && final.Status == "completed":
		if !jsonOutput {
			fmt.Fprintln(humanOut, "\nRun completed successfully.")
		}
		return nil
	case final.Status == "failed":
		// Treat a recorded exit code as authoritative. A failed job
		// without a result envelope means the run never reached the
		// container (build/dispatch error); return generic exit 1.
		msg := final.Error
		if msg == "" && followErr != nil {
			msg = followErr.Error()
		}
		if hasResult {
			if !jsonOutput {
				fmt.Fprintf(humanOut, "\nRun failed (exit code %d): %s\n", exitCode, msg)
			}
			return &exitCodeError{code: exitCode, msg: fmt.Sprintf("run failed with exit code %d: %s", exitCode, msg)}
		}
		if !jsonOutput {
			fmt.Fprintf(humanOut, "\nRun failed before the command executed: %s\n", msg)
		}
		return fmt.Errorf("run failed: %s", msg)
	default:
		// Defensive: non-terminal final status returned from GetStatus
		// after a context-cancelled follow. Surface the cancel.
		if followErr != nil {
			return followErr
		}
		return fmt.Errorf("run ended in unexpected status %q", final.Status)
	}
}

// runJSONResponse is the --json envelope written on stdout when a run
// reaches a terminal state (success or failure). ExitCode reflects the
// container's real exit code when the conductor recorded a result; 0
// otherwise (with Status='failed' as the signal).
type runJSONResponse struct {
	JobID    string `json:"jobId"`
	OSID     string `json:"osid,omitempty"`
	AppID    string `json:"appId"`
	SHA      string `json:"sha"`
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
}

// parseRunTimeout converts a user-supplied duration ("30m", "1h", "90s")
// into the integer-seconds shape the conductor's POST body expects. An
// empty input means "use the server's default" and returns 0 so the
// caller can omit timeoutSeconds from the body entirely.
//
// Refuses zero and negative durations: the user asked for a timeout, so
// a non-positive value is a typo, not a "wait forever" knob.
func parseRunTimeout(input string) (int, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(input)
	if err != nil {
		return 0, fmt.Errorf("--timeout %q is not a valid duration (e.g. 30m, 1h, 90s): %w", input, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--timeout must be positive, got %q", input)
	}
	secs := int(d.Round(time.Second).Seconds())
	if secs < 1 {
		// Sub-second resolution would round to 0; the conductor refuses
		// 0 and treats absent as "use default", so surface the typo.
		return 0, fmt.Errorf("--timeout %q rounds to under a second; pick at least 1s", input)
	}
	return secs, nil
}

// enforceVCSDeployType returns an error when the resolved app is not a
// VCS-deploy app. The brief explicitly scopes runos run to deployType
// 'vcs' (CLI-deploy apps have no meaningful commit SHA for run's
// build-on-demand path).
func enforceVCSDeployType(deployType string) error {
	if deployType == "vcs" {
		return nil
	}
	if deployType == "" {
		return fmt.Errorf("runos run requires a VCS-deploy app; this app has no deployType set (run `runos apps pull` to refresh)")
	}
	return fmt.Errorf("runos run is only valid for VCS-deployed apps; this app has deployType=%q. Use `runos deploy` for CLI-deploy apps", deployType)
}

// extractRunExitCode reads jobs.result.exitCode from a finished
// JobStatus. Returns (code, true) when the result envelope is present
// and decodable, (0, false) when no result was recorded yet (e.g. the
// orchestration failed before the container ran) so the caller can pick
// a generic exit-1 path instead of pretending a successful 0.
func extractRunExitCode(j *jobs.JobStatus) (int, bool) {
	if j == nil {
		return 0, false
	}
	r, err := j.RunResult()
	if err != nil || r == nil {
		return 0, false
	}
	return r.ExitCode, true
}

// joinCommand renders a command slice for display. Keeps each token as
// the user typed it (no shell-quoting); only used in the confirmation
// summary and the "Running ..." banner, never serialised back to the
// wire.
func joinCommand(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	return strings.Join(cmd, " ")
}

// buildRunSummary returns the pre-dispatch summary block. timeoutSeconds=0
// renders as "<server default>" so users can see they didn't override it.
func buildRunSummary(appID, cid, aid, sha string, command []string, timeoutSeconds int) string {
	timeoutLine := "<server default>"
	if timeoutSeconds > 0 {
		timeoutLine = (time.Duration(timeoutSeconds) * time.Second).String()
	}
	return fmt.Sprintf(`Run plan:
  App:      %s
  Cluster:  %s
  Account:  %s
  SHA:      %s
  Command:  %s
  Timeout:  %s
`, appID, cid, aid, shortSHA(sha), joinCommand(command), timeoutLine)
}

// Compile-time guard: ensure exitCodeError satisfies the interface
// Execute() uses to recover the propagated exit code. If the contract
// drifts (rename, signature change) this fails to compile rather than
// silently degrading to exit 1.
var _ interface{ ExitCode() int } = (*exitCodeError)(nil)
