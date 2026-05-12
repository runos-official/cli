package jobs

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	maxResultLength = 80
)

// DisplayStatus prints a one-shot summary of the job's current status.
// Used by `runos jobs show`. Line-oriented and stable; safe for CI logs
// and LLM consumers.
func DisplayStatus(job *JobStatus) {
	fmt.Printf("Job:      %s\n", job.ID)
	fmt.Printf("Type:     %s\n", job.Type)
	fmt.Printf("Status:   %s\n", job.Status)
	fmt.Printf("Progress: %s\n", job.Progress)

	if job.Error != "" {
		fmt.Printf("Error:    %s\n", job.Error)
	}
}

// FollowState carries the "last seen" snapshot the polling loop uses to
// emit deltas instead of repaints. Persists across iterations of
// FollowJobWithService.
//
// JobStatus is the previous job-level status string ("running",
// "completed", ...). JobProgress is the previous progress string.
// ItemStatus maps work-item id to its previous status so we can detect
// transitions (and surface only when one changes). ItemLogCursor maps
// work-item id to the last log entry id we printed, so each tick of the
// poller only fetches new lines.
type FollowState struct {
	JobStatus     string
	JobProgress   string
	ItemStatus    map[string]string
	ItemLogCursor map[string]string
}

// NewFollowState returns a fresh state ready for the first poll. All
// fields are empty so the first emit is treated as a transition.
func NewFollowState() *FollowState {
	return &FollowState{
		ItemStatus:    map[string]string{},
		ItemLogCursor: map[string]string{},
	}
}

// EmitFollowDeltas writes one line per state change since the previous
// poll to w. No screen-clear, no spinner, no repaint. Designed for CI
// logs and LLM consumers where every byte ends up in the transcript.
//
// Lines:
//   - "job <id>: <status> (<progress>)" when job-level status or
//     progress changed.
//   - "step <n> <name>: <status>" when a work item appears or its
//     status transitions. Terminal item statuses (completed/failed)
//     also include the result if non-empty, truncated to one line.
//   - "job <id>: failed: <error>" on terminal failure (the job-level
//     status line carries the error message).
//
// The state map is mutated in-place to record the new last-seen values,
// so the next call only emits genuine deltas. w is typically os.Stdout
// when called from FollowJobWithService; tests pass a bytes.Buffer.
//
// Pairs with EmitFollowLogs: callers that want both should use the
// combined EmitFollowDeltasWithLogs to preserve causal ordering
// (status of step N → logs of step N → status of step N+1). Calling
// EmitFollowDeltas + EmitFollowLogs in sequence produces the I2-2a
// "all statuses, then all logs" interleaving where step 6 / step 7
// completion lines print before step 6's own progress logs.
func EmitFollowDeltas(w io.Writer, job *JobStatus, items []WorkItem, state *FollowState) {
	emitJobLevel(w, job, state)
	for _, item := range sortedByStep(items) {
		emitItemStatus(w, item, state)
	}
}

// EmitFollowLogs prints any new work-item log lines since the previous
// poll, indented under their parent step. Lines come from the conductor's
// work-item-logs endpoint which is fed by `jobState.log()` calls in the
// orchestration (and, for VCS deploys, by the cluster-agent's build log
// table forwarded through the parent job).
//
// Skips work items in `pending` state (no logs to fetch) and any item
// whose status hasn't transitioned yet (the very first poll). Advances
// state.ItemLogCursor in-place so the next call only requests new lines.
//
// Errors fetching logs are best-effort: a single failed call doesn't
// abort the deploy, it just means logs may appear on a later tick.
func EmitFollowLogs(w io.Writer, svc *Service, jobID string, items []WorkItem, state *FollowState) {
	for _, item := range sortedByStep(items) {
		emitItemLogs(w, svc, jobID, item, state)
	}
}

// EmitFollowDeltasWithLogs is the causal-ordering variant: each work
// item's status transition is followed immediately by its own new log
// lines before the next item is considered. Job-level status emits
// once at the top.
//
// Regression target: I2-2a (TEST_LOG.md). The previous EmitFollowDeltas
// + EmitFollowLogs sequence emitted all status lines before any logs,
// so step N's progress logs landed underneath step N+1's "completed"
// header in the live transcript. With this combined emitter, every
// step's logs appear directly under its own status line.
func EmitFollowDeltasWithLogs(w io.Writer, svc *Service, jobID string, job *JobStatus, items []WorkItem, state *FollowState) {
	emitJobLevel(w, job, state)
	for _, item := range sortedByStep(items) {
		emitItemStatus(w, item, state)
		emitItemLogs(w, svc, jobID, item, state)
	}
}

func emitJobLevel(w io.Writer, job *JobStatus, state *FollowState) {
	if job.Status == state.JobStatus && job.Progress == state.JobProgress {
		return
	}
	line := fmt.Sprintf("job %s: %s", job.ID, job.Status)
	if job.Progress != "" {
		line += " (" + job.Progress + ")"
	}
	if job.Status == "failed" && job.Error != "" {
		line += ": " + job.Error
	}
	fmt.Fprintln(w, line)
	state.JobStatus = job.Status
	state.JobProgress = job.Progress
}

func emitItemStatus(w io.Writer, item WorkItem, state *FollowState) {
	prev, seen := state.ItemStatus[item.ID]
	if seen && prev == item.Status {
		return
	}
	line := fmt.Sprintf("step %d %s: %s", item.StepNumber, item.Name, item.Status)
	if item.Status == "completed" || item.Status == "failed" {
		if r := truncResult(item.Result()); r != "" {
			line += " (" + r + ")"
		}
	}
	fmt.Fprintln(w, line)
	state.ItemStatus[item.ID] = item.Status
}

func emitItemLogs(w io.Writer, svc *Service, jobID string, item WorkItem, state *FollowState) {
	if item.Status == "pending" || item.Status == "" {
		return
	}
	cursor := state.ItemLogCursor[item.ID]
	resp, err := svc.GetWorkItemLogs(jobID, item.ID, cursor)
	if err != nil {
		return
	}
	for _, entry := range resp.Logs {
		line := strings.TrimRight(entry.Message, "\n")
		fmt.Fprintf(w, "  %s\n", line)
	}
	if resp.NextCursor != nil {
		state.ItemLogCursor[item.ID] = *resp.NextCursor
	}
}

// sortedByStep returns a copy of items sorted ascending by StepNumber.
// Stable order so deltas read top-to-bottom in the same order the user
// expects across multiple polls.
func sortedByStep(items []WorkItem) []WorkItem {
	out := make([]WorkItem, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool {
		return out[i].StepNumber < out[j].StepNumber
	})
	return out
}

// truncResult collapses a result string to a single line and truncates
// it for the per-step log output. Multi-line results get the first
// line; long results get the leading slice plus an ellipsis.
func truncResult(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return ""
	}
	if idx := strings.IndexByte(result, '\n'); idx > 0 {
		result = result[:idx]
	}
	if len(result) > maxResultLength {
		result = result[:maxResultLength-3] + "..."
	}
	return result
}
