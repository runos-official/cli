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
// transitions (and surface only when one changes).
type FollowState struct {
	JobStatus   string
	JobProgress string
	ItemStatus  map[string]string
}

// NewFollowState returns a fresh state ready for the first poll. All
// fields are empty so the first emit is treated as a transition.
func NewFollowState() *FollowState {
	return &FollowState{ItemStatus: map[string]string{}}
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
func EmitFollowDeltas(w io.Writer, job *JobStatus, items []WorkItem, state *FollowState) {
	// Job-level status / progress: emit when either side changes.
	if job.Status != state.JobStatus || job.Progress != state.JobProgress {
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

	// Work items: stable order by step number so logs read top-to-
	// bottom in the same order the user expects.
	sorted := make([]WorkItem, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].StepNumber < sorted[j].StepNumber
	})

	for i := range sorted {
		item := &sorted[i]
		prev, seen := state.ItemStatus[item.ID]
		if seen && prev == item.Status {
			continue
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
