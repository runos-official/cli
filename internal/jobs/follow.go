package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	pollInterval = 1 * time.Second
)

// FollowJob polls a job until completion, emitting one line per state
// change to os.Stdout. Suitable for CI and LLM consumers; no terminal
// escape codes, no spinners, no repaints.
func FollowJob(jobID string) error {
	return FollowJobToWriter(jobID, os.Stdout)
}

// FollowJobToWriter is like FollowJob but routes the per-state-change
// progress lines to w instead of os.Stdout. Callers that want job
// progress on stderr (typical: --json mode, where stdout is reserved
// for the final JSON envelope) pass os.Stderr here. The terminal state
// line is still emitted to w before the function returns.
func FollowJobToWriter(jobID string, w io.Writer) error {
	svc, err := NewService()
	if err != nil {
		return err
	}

	// Default timeout of 30 minutes for job following
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	return FollowJobWithServiceToWriter(ctx, svc, jobID, w)
}

// FollowJobWithService polls a job using the provided Service until it
// reaches a terminal state, emitting deltas via EmitFollowDeltas to
// os.Stdout. Use FollowJobWithServiceToWriter when you need to control
// the destination (e.g. --json mode → stderr).
//
// Returns nil on terminal "completed", a non-nil error on terminal
// "failed" (containing the job's error message). The terminal state
// line itself is emitted before this function returns, so CI logs end
// with the final job line whichever way it goes.
func FollowJobWithService(ctx context.Context, svc *Service, jobID string) error {
	return FollowJobWithServiceToWriter(ctx, svc, jobID, os.Stdout)
}

// FollowJobWithServiceToWriter is the writer-parameterised variant of
// FollowJobWithService. All other behaviour matches.
func FollowJobWithServiceToWriter(ctx context.Context, svc *Service, jobID string, w io.Writer) error {
	state := NewFollowState()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("job follow cancelled: %w", ctx.Err())
		default:
		}

		job, err := svc.GetStatus(jobID)
		if err != nil {
			return err
		}

		items, err := svc.GetWorkItems(jobID)
		if err != nil {
			return err
		}

		EmitFollowDeltasWithLogs(w, svc, jobID, job, items.WorkItems, state)

		if job.IsTerminal() {
			if job.Status == "failed" {
				return fmt.Errorf("job failed: %s", job.Error)
			}
			return nil
		}

		time.Sleep(pollInterval)
	}
}
