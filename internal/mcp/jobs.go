package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// staticJobsTools registers the `runos follow <jobId>` static MCP tool.
// Lives outside the manifest because the underlying CLI verb does
// long-poll streaming until the job terminates — a shape the manifest's
// generic dispatcher (one HTTP call, one response) doesn't model.
//
// MCP semantics: the tool blocks until the job reaches a terminal state
// (success / failed / cancelled) or the 10-minute subprocess timeout
// fires, whichever comes first, and returns the streamed progress as a
// single text payload. Read-only — the verb just watches the job, it
// doesn't mutate cluster state.
// Registered on every server that can create a job, not just the read
// one. Pre-fix only `read` carried it, so the write servers dispatched
// async work they had no way to watch, and an agent driving a write
// server had to fall back to polling jobs_show. Regression target:
// goal 19 A13.
func staticJobsTools(category string) []Tool {
	switch category {
	case "read", "write", "sensitive_write":
	default:
		return nil
	}

	return []Tool{
		{
			Name: "jobs_follow",
			Description: `Follow a job's progress until it completes (or fails/cancels).

Blocks for the duration of the job — useful when an LLM dispatched an async operation (apps_sync, services_sync, deploy without --follow) and wants the same end-of-rollout signal a human gets from "runos follow <jobId>". Returns the streamed work-item log + final status as a single text payload.

Most async operations return a jobId in their response (apps_sync's "Follow rollout: runos follow <id>" hint, deploy's job id, etc.). Pass that jobId here and wait for terminal state. The 10-minute subprocess timeout fires if the job hasn't finished.

This is the canonical "wait until done" verb for orchestrations, preferable to polling jobs_show in a loop (cheaper, doesn't burn the LLM's prompt cache).`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"job_id": {
						Type:        "string",
						Description: "Job ID to follow (UUID). Get this from the response of an async operation like apps_sync, services_sync, or deploy.",
					},
				},
				Required: []string{"job_id"},
			},
		},
	}
}

// isStaticJobsTool reports whether toolName is the jobs_follow static
// tool (rather than a manifest-driven jobs_* command like jobs_list /
// jobs_show).
func isStaticJobsTool(toolName string) bool {
	return toolName == "jobs_follow"
}

// handleJobsCommand dispatches the jobs_follow tool to a runos
// subprocess. Mirrors handleAppsCommand: 10-minute timeout, stdout +
// stderr capture, lockstep with the CLI binary already running the MCP
// server.
func (s *Server) handleJobsCommand(toolName string, args map[string]any) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	jobID, ok := stringArg(args, "job_id")
	if !ok || jobID == "" {
		return "", fmt.Errorf("%s: job_id is required", toolName)
	}

	cmdArgs := []string{"follow", jobID}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if runErr != nil {
		if output == "" {
			return "", fmt.Errorf("%s failed: %w", toolName, runErr)
		}
		return "", fmt.Errorf("%s failed: %s", toolName, output)
	}

	return output, nil
}
