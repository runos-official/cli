package cmd

import (
	"github.com/spf13/cobra"
)

// jobsCmd is the static parent for the `runos jobs <subcommand>` tree.
// Mirrors clustersCmd / appsCmd / servicesCmd: registered in root.go
// before registerDynamicCommands so the manifest-driven jobs
// subcommands (list, show, cancel, workitems, workitem-logs) merge
// under this same parent. The static child `follow` lives outside the
// manifest because it long-polls until the job terminates — a shape
// the generic manifest dispatcher doesn't model.
var jobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "Manage jobs",
	Long:  `Manage RunOS jobs. Use subcommands to list, show, follow, cancel, or inspect work items.`,
}

// jobsFollowCmd is the static `runos jobs follow <jobId>` subcommand.
// Closes the parity gap with the MCP `jobs_follow` tool: both surfaces
// now expose the verb under the same `jobs` namespace. The CLI also
// keeps the top-level `runos follow` alias for backward compatibility
// (the MCP tool shells to it). Issue 105.
var jobsFollowCmd = &cobra.Command{
	Use:   "follow <job-id>",
	Short: "Follow a job's progress until completion",
	Long: `Follow a job's progress in real-time until it completes or fails.

Equivalent to the top-level ` + "`runos follow <job-id>`" + ` command;
provided under the ` + "`jobs`" + ` namespace so the CLI matches the
MCP ` + "`jobs_follow`" + ` tool surface.

With --json, the human-readable progress lines route to stderr and a
single JSON envelope describing the terminal job status emits on stdout
when the job finishes.

Example:
  runos jobs follow abc123-def456-...`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runFollow,
}

func init() {
	jobsFollowCmd.Flags().BoolP("json", "j", false, "Emit terminal job status as a JSON envelope on stdout; route human progress lines to stderr")
	jobsCmd.AddCommand(jobsFollowCmd)
}
