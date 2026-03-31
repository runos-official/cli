package jobs

import (
	"context"
	"fmt"
	"time"
)

const (
	pollInterval = 1 * time.Second
)

// FollowJob polls a job until completion, displaying live progress to the terminal.
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

// FollowJobWithService polls a job using the provided Service until it reaches a terminal state.
// The context allows callers to cancel or set timeouts on the polling loop.
func FollowJobWithService(ctx context.Context, svc *Service, jobID string) error {
	spinnerIdx := 0

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

		isFinal := job.IsTerminal()

		DisplayFollow(job, items.WorkItems, spinnerIdx, isFinal)

		if isFinal {
			if job.Status == "failed" {
				return fmt.Errorf("job failed: %s", job.Error)
			}
			return nil
		}

		spinnerIdx++
		time.Sleep(pollInterval)
	}
}
