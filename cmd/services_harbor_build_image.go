package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/buildargs"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/harbor"
	"github.com/runos-official/cli/internal/jobs"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var servicesHarborBuildImageCmd = &cobra.Command{
	Use:   "build-image",
	Short: "Build a local context and push it to the system Harbor's runos-apps project",
	Long: `Build an arbitrary container image from a LOCAL build context and push it
into the cluster's system Harbor under the managed runos-apps project,
using Harbor's managed push credentials.

This is decoupled from apps, deployments, and VCS: there is no app id, no
commit SHA, and no phantom build-only app. The --context directory is
tarballed (honoring .dockerignore) and uploaded directly to the cluster
agent, which builds it on the same in-cluster BuildKit that builds app
images and pushes runos-apps/<repo>:<tag> for every --tag.

The project is fixed to runos-apps and is NOT a flag; --repo is the
repository name within it. Mutable tags (e.g. :latest) are overwritten
silently, like a normal docker push.

The verb follows the CLI's job convention: it returns immediately after
queueing by default. Pass --follow to stream build progress and block
until the build finishes, propagating a non-zero exit on build failure so
a CI step gates on the build outcome.

--build-arg mirrors runos deploy: repeated KEY=VALUE flags are forwarded
to the image build. Duplicate keys within a single invocation are
rejected.

Examples:
  runos services harbor build-image --cid mycluster \
    --context apps/backend/docker/vm-workspace \
    --repo acme-vm-workspace --tag latest --follow

  runos services harbor build-image --cid mycluster --context . \
    --repo my-tool --tag v1 --tag latest --build-arg GO_VERSION=1.24`,
	SilenceUsage: true,
	RunE:         runServicesHarborBuildImage,
}

func init() {
	f := servicesHarborBuildImageCmd.Flags()
	f.String("cid", "", "cluster ID (the system Harbor is resolved from it; defaults to the configured default cluster)")
	f.String("context", "", "local build-context directory to tarball and upload (required)")
	f.String("dockerfile", "Dockerfile", "path to the Dockerfile, relative to --context")
	f.String("repo", "", "target repository name within the runos-apps project, no project prefix and no :tag (required)")
	f.StringArray("tag", nil, "image tag to push (repeatable; at least one required). Each yields runos-apps/<repo>:<tag>")
	f.StringArray("build-arg", nil, "Docker build arg `KEY=VALUE` (repeatable). Duplicate keys within a single invocation are rejected.")
	f.BoolP("follow", "f", false, "follow build progress until completion; without it, exits 0 the moment the conductor accepts the request")
	f.BoolP("json", "j", false, "emit a JSON envelope on stdout (jobId, repo, tags, images, and skippedBecauseCached/durationMs when --follow reaches a terminal state); humans on stderr")
	f.BoolP("yes", "y", false, "skip the confirmation prompt (auto-skipped when stdin is not a terminal, e.g. CI/MCP)")
	_ = servicesHarborBuildImageCmd.MarkFlagRequired("context")
	_ = servicesHarborBuildImageCmd.MarkFlagRequired("repo")
}

func runServicesHarborBuildImage(cmd *cobra.Command, args []string) (rerr error) {
	cmd.SilenceUsage = true

	jsonOutput, _ := cmd.Flags().GetBool("json")
	var jsonHandled bool
	defer func() {
		// Single JSON document on stdout: the failure branches that need to
		// carry job context write their own envelope and set jsonHandled so
		// this fallback (a flat {error,...} envelope) doesn't double-emit.
		if jsonOutput && rerr != nil && !jsonHandled {
			rerr = emitJSONError(cmd, rerr)
		}
	}()

	// Under --json, route human-readable progress to stderr so stdout stays
	// a single parseable document. Mirrors deploy / apps build --json.
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

	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	if cid == "" {
		return fmt.Errorf("cluster ID required: pass --cid or set a default with 'runos config set cid <cluster-id>'")
	}

	contextDir, _ := cmd.Flags().GetString("context")
	dockerfile, _ := cmd.Flags().GetString("dockerfile")
	repo, _ := cmd.Flags().GetString("repo")
	tags, _ := cmd.Flags().GetStringArray("tag")
	flagFollow, _ := cmd.Flags().GetBool("follow")
	flagYes, _ := cmd.Flags().GetBool("yes")

	if len(tags) == 0 {
		return fmt.Errorf("at least one --tag is required")
	}

	// Resolve the context directory and confirm it's a real directory.
	absContext, err := filepath.Abs(contextDir)
	if err != nil {
		return fmt.Errorf("resolve --context %q: %w", contextDir, err)
	}
	info, err := os.Stat(absContext)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("--context %q does not exist", contextDir)
		}
		return fmt.Errorf("stat --context %q: %w", contextDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--context %q is not a directory (a build context is a directory; a Dockerfile may COPY sibling files)", contextDir)
	}

	// Validate --dockerfile is inside the context and exists before any
	// network round-trip. Reuses the deploy-side resolver (rejects absolute
	// paths and ..-escape, checks the file is a regular file).
	if _, err := deploy.ResolveDockerfilePath(absContext, dockerfile); err != nil {
		return err
	}

	// Parse --build-arg with the same parser deploy uses, so the wire field
	// (buildArgsCli) is identical and dup-key/ARG-name errors stay close to
	// the user's argv.
	rawBuildArgs, _ := cmd.Flags().GetStringArray("build-arg")
	buildArgsCli, err := buildargs.Parse(rawBuildArgs)
	if err != nil {
		return err
	}

	if err := confirmBuildImage(harborBuildImageSummary(repo, absContext, cid, tags), flagYes); err != nil {
		return err
	}

	fmt.Fprintf(humanOut, "Archiving build context %s ...\n", absContext)
	tarball, err := deploy.CreateBuildContextTarball(absContext)
	if err != nil {
		return fmt.Errorf("failed to archive context: %w", err)
	}

	svc := harbor.NewService(cfg.GetAPIURL(), token, aid, cid)

	prep, err := svc.PrepareBuildImage(harbor.BuildImageRequest{
		Repo:         repo,
		Tags:         tags,
		Dockerfile:   dockerfile,
		BuildArgsCli: buildArgsCli,
	})
	if err != nil {
		return fmt.Errorf("failed to prepare build: %w", err)
	}

	fmt.Fprintf(humanOut, "Uploading context (%d bytes) ...\n", tarball.Len())
	if err := svc.UploadContext(prep.UploadURL, prep.Token, tarball); err != nil {
		return fmt.Errorf("failed to upload context: %w", err)
	}

	envelope := harborBuildImageJSONResponse{
		JobID:  prep.JobID,
		Repo:   repo,
		Tags:   tags,
		Images: prep.Images,
	}

	if !flagFollow {
		fmt.Fprintf(humanOut, "\nBuild queued:\n")
		fmt.Fprintf(humanOut, "  Job ID: %s\n", prep.JobID)
		for _, img := range prep.Images {
			fmt.Fprintf(humanOut, "  Image:  %s\n", img)
		}
		fmt.Fprintf(humanOut, "\nFollow build: runos follow %s\n", prep.JobID)
		if jsonOutput {
			return writeJSON(stdout, envelope)
		}
		return nil
	}

	fmt.Fprintln(humanOut, "\nFollowing build progress...")

	jobSvc, err := jobs.NewService()
	if err != nil {
		return err
	}

	// 30-minute follow ceiling, matching the conductor orchestration ceiling
	// and FollowJobToWriter's default.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	followErr := jobs.FollowJobWithServiceToWriter(ctx, jobSvc, prep.JobID, humanOut)

	final, getErr := jobSvc.GetStatus(prep.JobID)
	if getErr != nil {
		if followErr != nil {
			return followErr
		}
		return getErr
	}

	build, _ := final.BuildResult()
	if build != nil {
		envelope.DurationMs = build.DurationMs
		cached := build.SkippedBecauseCached
		envelope.SkippedBecauseCached = &cached
	}

	switch {
	case followErr == nil && final.Status == "completed":
		summarizeHarborBuildImage(humanOut, prep.Images, build)
		if jsonOutput {
			return writeJSON(stdout, envelope)
		}
		return nil
	case final.Status == "failed":
		msg := final.Error
		if msg == "" && followErr != nil {
			msg = followErr.Error()
		}
		if jsonOutput {
			// Emit a single envelope carrying the job context + error, then
			// suppress the deferred flat-error fallback.
			envelope.Error = msg
			_ = writeJSON(stdout, envelope)
			jsonHandled = true
		} else {
			fmt.Fprintf(humanOut, "\nBuild failed: %s\n", msg)
		}
		return fmt.Errorf("build failed: %s", msg)
	default:
		if followErr != nil {
			return followErr
		}
		return fmt.Errorf("build ended in unexpected status %q", final.Status)
	}
}

// harborBuildImageJSONResponse is the --json envelope on stdout. The two
// result fields populate only when --follow reaches a terminal state and
// the conductor's jobs.result is present. SkippedBecauseCached is *bool so
// omitempty distinguishes "absent" (no --follow yet) from explicit false
// (always false in v1: the build never short-circuits on a cache).
type harborBuildImageJSONResponse struct {
	JobID                string   `json:"jobId"`
	Repo                 string   `json:"repo"`
	Tags                 []string `json:"tags"`
	Images               []string `json:"images,omitempty"`
	SkippedBecauseCached *bool    `json:"skippedBecauseCached,omitempty"`
	DurationMs           int64    `json:"durationMs,omitempty"`
	Error                string   `json:"error,omitempty"`
}

// harborBuildImageSummary renders the pre-build confirmation block. images
// aren't known until prepare returns, so the summary shows the project +
// repo + tags the refs will be derived from.
func harborBuildImageSummary(repo, contextDir, cid string, tags []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Build and push to the system Harbor (project runos-apps):\n")
	fmt.Fprintf(&b, "  context: %s\n", contextDir)
	fmt.Fprintf(&b, "  repo:    runos-apps/%s\n", repo)
	fmt.Fprintf(&b, "  tags:    %s\n", strings.Join(tags, ", "))
	fmt.Fprintf(&b, "  cluster: %s\n", cid)
	return b.String()
}

// summarizeHarborBuildImage prints the terminal-state summary on success.
func summarizeHarborBuildImage(w io.Writer, images []string, r *jobs.BuildResult) {
	if len(images) == 0 {
		fmt.Fprintln(w, "\nBuild complete.")
		return
	}
	if r != nil && r.DurationMs > 0 {
		fmt.Fprintf(w, "\nPushed %s in %s.\n", strings.Join(images, ", "), formatBuildDuration(r.DurationMs))
		return
	}
	fmt.Fprintf(w, "\nPushed %s.\n", strings.Join(images, ", "))
}

// formatBuildDuration renders a millisecond duration as a short string.
func formatBuildDuration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

// confirmBuildImage prompts before building unless skipped (--yes) or
// stdin is not a terminal (CI/MCP). Mirrors confirmDeploy's shape with
// build-specific wording.
func confirmBuildImage(summary string, skipPrompt bool) error {
	if skipPrompt {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	fmt.Fprint(os.Stderr, summary)
	ok, err := confirm("\nProceed with build? [y/N] ")
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if !ok {
		return fmt.Errorf("build cancelled")
	}
	return nil
}
