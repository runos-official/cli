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
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runFollow,
}

func runFollow(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		fmt.Println("No job ID provided.")
		fmt.Println()
		fmt.Println("To get a list of jobs, run:")
		fmt.Println("  runos jobs list")
		fmt.Println()
		fmt.Println("Then follow a specific job with:")
		fmt.Println("  runos follow <job-id>")
		return nil
	}

	jobID := args[0]
	if !jobs.ValidateJobID(jobID) {
		return fmt.Errorf("invalid job id %q: expected UUID shape (8-4-4-4-12 hex). Use 'runos jobs list' to find the id you want", jobID)
	}
	return jobs.FollowJob(jobID)
}
