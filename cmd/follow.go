package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/runos-official/cli/internal/jobs"

	"github.com/spf13/cobra"
)

var followCmd = &cobra.Command{
	Use:   "follow <job-id>",
	Short: "Follow a job's progress until completion",
	Long: `Follow a job's progress in real-time until it completes or fails.

To get a list of job IDs, use:
  runos jobs list

Example:
  runos follow abc123-def456-...

With --json, the human-readable progress lines route to stderr and a
single JSON envelope describing the terminal job status emits on stdout
when the job finishes, so CI pipelines can pipe stdout into jq while
still observing progress via stderr.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runFollow,
}

func init() {
	followCmd.Flags().BoolP("json", "j", false, "Emit terminal job status as a JSON envelope on stdout; route human progress lines to stderr")
}

func runFollow(cmd *cobra.Command, args []string) error {
	jobID := args[0]
	if jobID == "" {
		// Args:ExactArgs(1) accepts an explicit "" positional, but an
		// empty job id is a user error (CI doing `runos follow "$JOB_ID"`
		// with JOB_ID unset). Surface a clear diagnostic instead of
		// silently hitting jobs.ValidateJobID's UUID-shape message.
		return fmt.Errorf("job id is required, got empty value. Use 'runos jobs list' to find the id you want")
	}
	if !jobs.ValidateJobID(jobID) {
		return fmt.Errorf("invalid job id %q: expected UUID shape (8-4-4-4-12 hex). Use 'runos jobs list' to find the id you want", jobID)
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Standalone follow carries only a job id, with no context about
	// how the job was created. FollowAnyJobToWriterResult tries the
	// sanitized deployment-progress endpoint first (so a scoped machine
	// key following its own deploy job never needs broader job-read
	// authority, story 346 D3) and falls back to the general
	// job/work-item/log follow ONLY when that very first read answers
	// the one documented ambiguous refusal (404: not found / no scoped
	// binding / legacy job). Any other refusal (403, 503, a transport
	// error) is final, exactly as it is for `deploy --follow`: see
	// FollowAnyJobToWriterResult's doc comment for the full rule.
	svc, err := jobs.NewService()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	followWriter := io.Writer(os.Stdout)
	if jsonOutput {
		followWriter = os.Stderr
	}
	result, followErr := jobs.FollowAnyJobToWriterResult(ctx, svc, jobID, followWriter)

	if !jsonOutput {
		return followErr
	}

	// --json: human progress already went to stderr above; the final
	// JobStatus shape (id/type/status/progress/currentStep/error) still
	// emits as a single JSON document on stdout, exactly as before
	// (finding 1351: switching the underlying read to the sanitized
	// progress endpoint must not change the published --json contract).
	// jobs.ProgressToJobStatus maps a progress-served follow into that
	// same shape; the raw DeploymentProgress fields are never emitted
	// here. Mirrors the post-#93 deploy --follow --json contract so CI
	// consumers can pipe stdout into jq. Issue 99.
	var terminal *jobs.JobStatus
	switch {
	case result != nil && result.Progress != nil:
		terminal = jobs.ProgressToJobStatus(result.Progress)
	case result != nil && result.Job != nil:
		terminal = result.Job
	default:
		return followErr
	}
	out, mErr := json.MarshalIndent(terminal, "", "  ")
	if mErr != nil {
		if followErr != nil {
			return followErr
		}
		return mErr
	}
	fmt.Println(string(out))
	return followErr
}
