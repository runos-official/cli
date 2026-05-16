package cmd

import (
	"fmt"

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
  runos follow abc123-def456-...`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runFollow,
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
	return jobs.FollowJob(jobID)
}
