package jobs

import "encoding/json"

// JobStatus represents the current state of a job returned by the API.
type JobStatus struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Progress    string `json:"progress"`
	CurrentStep string `json:"currentStep"`
	Error       string `json:"error,omitempty"`
	// RawResult is the conductor's `result` field as raw JSON. Decoded
	// per-job-type by the caller (e.g. `runos run` reads { exitCode,
	// durationMs, imageTag } from app.run jobs to propagate the
	// container's real exit code). Stays as json.RawMessage so future
	// job types can carry their own result shapes without touching this
	// struct.
	RawResult json.RawMessage `json:"result,omitempty"`
}

// RunResult is the shape of jobs.result for type=app.run jobs.
// exitCode is the container's real terminated exit code (0 on success,
// non-zero on failure, 124 on timeout per the cluster-agent contract).
type RunResult struct {
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs,omitempty"`
	ImageTag   string `json:"imageTag,omitempty"`
}

// RunResult parses RawResult as an app.run result envelope. Returns
// (nil, nil) when no result is set yet (job still in flight), or a
// parse error wrapped with context.
func (j *JobStatus) RunResult() (*RunResult, error) {
	if len(j.RawResult) == 0 {
		return nil, nil
	}
	var r RunResult
	if err := json.Unmarshal(j.RawResult, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// BuildResult is the shape of jobs.result for type=app.build jobs
// (conductor objective 43 / story 73). ImageTag is the Harbor tag the
// build pushed (or would have pushed in the cached case); BuiltAt is
// an RFC3339 timestamp; DurationMs is orchestration wall time;
// SkippedBecauseCached is true when Harbor already had the image and
// fetch/resolve/build steps short-circuited.
type BuildResult struct {
	ImageTag             string `json:"imageTag,omitempty"`
	BuiltAt              string `json:"builtAt,omitempty"`
	DurationMs           int64  `json:"durationMs,omitempty"`
	SkippedBecauseCached bool   `json:"skippedBecauseCached"`
}

// BuildResult parses RawResult as an app.build result envelope. Returns
// (nil, nil) when no result is set yet (job still in flight), or a
// parse error wrapped with context. Mirrors RunResult.
func (j *JobStatus) BuildResult() (*BuildResult, error) {
	if len(j.RawResult) == 0 {
		return nil, nil
	}
	var r BuildResult
	if err := json.Unmarshal(j.RawResult, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// WorkItem represents a single step in a job's execution pipeline.
type WorkItem struct {
	ID          string          `json:"id"`
	JobID       string          `json:"jobId"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	StepNumber  int             `json:"stepNumber"`
	RawResult   json.RawMessage `json:"result,omitempty"`
	CreatedAt   string          `json:"createdAt"`
	StartedAt   string          `json:"startedAt,omitempty"`
	CompletedAt string          `json:"completedAt,omitempty"`
}

// Result returns the work item's result as a string, handling both string and JSON object types.
func (w *WorkItem) Result() string {
	if len(w.RawResult) == 0 {
		return ""
	}

	// Try to unmarshal as a string first
	var str string
	if err := json.Unmarshal(w.RawResult, &str); err == nil {
		return str
	}

	// If not a string, return the raw JSON
	return string(w.RawResult)
}

// WorkItemsResponse represents the API response containing work items for a job.
type WorkItemsResponse struct {
	JobID     string     `json:"jobId"`
	WorkItems []WorkItem `json:"workItems"`
	HasMore   bool       `json:"hasMore"`
}

// IsTerminal reports whether the job has reached a final state (completed or failed).
func (j *JobStatus) IsTerminal() bool {
	return j.Status == "completed" || j.Status == "failed"
}

// WorkItemLog is one log line emitted by an in-flight work item. Used by
// the follow poller to stream cluster-agent build progress and any other
// per-step output back to the CLI's stdout.
type WorkItemLog struct {
	ID         string `json:"id"`
	WorkItemID string `json:"workItemId"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	CreatedAt  string `json:"createdAt"`
}

// WorkItemLogsResponse is the API response for /jobs/:id/workitems/:wid/logs.
// NextCursor is the ID to pass as `after` on the next request.
type WorkItemLogsResponse struct {
	WorkItemID string        `json:"workItemId"`
	Logs       []WorkItemLog `json:"logs"`
	NextCursor *string       `json:"nextCursor"`
}
