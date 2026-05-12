package jobs

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	pollInterval = 1 * time.Second
)

// FollowJob polls a job until completion, emitting one line per state
// change. Suitable for CI and LLM consumers; no terminal escape codes,
// no spinners, no repaints.
func FollowJob(jobID string) error {
	svc, err := NewService()
	if err != nil {
		return err
	}

	// Default timeout of 30 minutes for job following
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	return FollowJobWithService(ctx, svc, jobID)
}

// FollowJobWithService polls a job using the provided Service until it
// reaches a terminal state, emitting deltas via EmitFollowDeltas. The
// context allows callers to cancel or set timeouts on the polling loop.
//
// Returns nil on terminal "completed", a non-nil error on terminal
// "failed" (containing the job's error message). The terminal state
// line itself is emitted before this function returns, so CI logs end
// with the final job line whichever way it goes.
func FollowJobWithService(ctx context.Context, svc *Service, jobID string) error {
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

		EmitFollowDeltasWithLogs(os.Stdout, svc, jobID, job, items.WorkItems, state)

		if job.IsTerminal() {
			if job.Status == "failed" {
				return fmt.Errorf("job failed: %s", job.Error)
			}
			return nil
		}

		time.Sleep(pollInterval)
	}
}
