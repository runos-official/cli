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

// WaitForJob polls a job using the provided Service until it reaches a
// terminal state, WITHOUT emitting any per-step progress to stdout. The
// silent counterpart to FollowJobWithService, intended for non-TTY callers
// that still need to block on the job's outcome.
//
// Returns nil on terminal "completed", a non-nil error on terminal
// "failed" carrying the conductor's job.Error string. Propagates context
// cancellation / deadline as a wrapped error.
//
// V12 fix (VCS_DEPLOY_TEST_NOTES.md): pre-fix, apps_sync printed
// `env: replace ok (job <id>)` the moment the conductor accepted the
// API call and exited 0 even when the underlying cluster job failed
// (e.g. `kubectl patch` against a missing configmap). The CLI must
// observe the job's terminal status before declaring success; this
// helper is what apps_sync dispatches into in non-TTY (CI) mode. TTY
// callers go through FollowJobWithService instead so users see streamed
// progress lines.
func WaitForJob(ctx context.Context, svc *Service, jobID string) error {
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

		if job.IsTerminal() {
			if job.Status == "failed" {
				return fmt.Errorf("job failed: %s", job.Error)
			}
			return nil
		}

		time.Sleep(pollInterval)
	}
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

		EmitFollowDeltas(os.Stdout, job, items.WorkItems, state)
		EmitFollowLogs(os.Stdout, svc, jobID, items.WorkItems, state)

		if job.IsTerminal() {
			if job.Status == "failed" {
				return fmt.Errorf("job failed: %s", job.Error)
			}
			return nil
		}

		time.Sleep(pollInterval)
	}
}
