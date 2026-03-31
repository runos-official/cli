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
