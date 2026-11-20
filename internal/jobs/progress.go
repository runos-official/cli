package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// DeploymentProgress is the minimal runos.deployment-progress.v1
// response from GET /jobs/:jobId/deployment-progress (handbook article
// conductor-api/jobs/get-deployment-progress, story 340/FPL431). It
// carries bound identifiers, states, timings and the published stage
// enum only: no application logs, raw work-item output, job input or
// result, actor, or credential metadata. Decoded with an explicit
// allowlist (no catch-all RawBody, unlike JobStatus): any field this
// contract does not name is not a field this CLI should ever display.
type DeploymentProgress struct {
	Version    string          `json:"version"`
	JobID      string          `json:"jobId"`
	AppID      string          `json:"appId"`
	State      string          `json:"state"`
	StartedAt  string          `json:"startedAt"`
	UpdatedAt  string          `json:"updatedAt"`
	FinishedAt *string         `json:"finishedAt"`
	Stages     []ProgressStage `json:"stages"`
}

// ProgressStage is one entry of DeploymentProgress.Stages. State is the
// same six-value enum as the job's own State; Message is the same
// state spelled from the published stage-message enum
// (pending/started/completed/failed/cancelled/timed_out), never a
// free-text sentence.
type ProgressStage struct {
	ID         string  `json:"id"`
	State      string  `json:"state"`
	StartedAt  *string `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
	Message    string  `json:"message"`
}

// progressTerminalStates mirrors the published state enum's terminal
// members. Unlike JobStatus.IsTerminal, this also recognises
// "timed_out", which the deployment-progress contract publishes as a
// distinct terminal state.
func isProgressTerminalState(state string) bool {
	switch state {
	case "completed", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

// IsTerminal reports whether this progress snapshot's job has reached
// a final state.
func (p *DeploymentProgress) IsTerminal() bool {
	return isProgressTerminalState(p.State)
}

// ProgressError is returned by GetDeploymentProgress for a non-2xx
// response. Callers branch on StatusCode: a 404 covers every refusal
// the endpoint intentionally cannot distinguish (missing job, no
// scoped binding, lineage mismatch, credential mismatch, unresolved
// app -- see the endpoint article's Refusals section), so it is safe
// for a caller to treat it as "not a scoped deploy job" and fall back
// to a differently-authorized read. A 403/503 is a definitive refusal
// of the credential that DOES own this job (revoked, expired, or the
// agent-side stage-recheck capability still closed) and must never be
// retried through a broader route (story 346, D4).
type ProgressError struct {
	StatusCode int
	Body       []byte
}

// Error renders the one-liner every caller that only prints gets.
func (e *ProgressError) Error() string {
	return fmt.Sprintf("deployment progress refused (%d): %s", e.StatusCode, string(e.Body))
}

// GetDeploymentProgress reads the sanitized own-deployment progress
// for jobID: GET /jobs/:jobId/deployment-progress. The route carries
// only the job id -- no account, cluster or app segment -- because the
// account, cluster and app are resolved server-side from the job's own
// stored, verified authority binding, never from a client-supplied
// value (story 340/FPL431).
func (s *Service) GetDeploymentProgress(jobID string) (*DeploymentProgress, error) {
	reqURL := fmt.Sprintf("%s/jobs/%s/deployment-progress", s.baseURL, url.PathEscape(jobID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &ProgressError{StatusCode: resp.StatusCode, Body: body}
	}

	var progress DeploymentProgress
	if err := json.Unmarshal(body, &progress); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	// Finding 1328: the endpoint resolves the job entirely from the
	// path segment, so a response naming a different job id is never
	// a legitimate answer to this request -- treat it as a protocol
	// violation rather than trusting it into a caller's local state.
	if progress.JobID != "" && progress.JobID != jobID {
		return nil, fmt.Errorf("deployment progress response named job %q, requested %q", progress.JobID, jobID)
	}
	return &progress, nil
}
