package jobs

import (
	"context"
	"fmt"
	"io"
	"time"
)

// ProgressFollowState is FollowState's analogue for the sanitized
// deployment-progress endpoint. Progress carries no work items and no
// raw logs to diff against, so the only "last seen" state worth
// tracking is the job's own top-level state and each stage's state.
type ProgressFollowState struct {
	JobState   string
	StageState map[string]string
}

// NewProgressFollowState returns a fresh state ready for the first
// poll; every field is empty so the first emit is treated as a
// transition.
func NewProgressFollowState() *ProgressFollowState {
	return &ProgressFollowState{StageState: map[string]string{}}
}

// emitProgressDelta writes one line per state change since the
// previous poll to w, mirroring EmitFollowDeltas's no-repaint
// contract. There is no log tier to stream underneath a stage line:
// runos.deployment-progress.v1 carries no raw output by design.
func emitProgressDelta(w io.Writer, p *DeploymentProgress, state *ProgressFollowState) {
	if p.State != state.JobState {
		fmt.Fprintf(w, "job %s: %s\n", p.JobID, p.State)
		state.JobState = p.State
	}
	for _, stage := range p.Stages {
		if prev, seen := state.StageState[stage.ID]; seen && prev == stage.State {
			continue
		}
		fmt.Fprintf(w, "stage %s: %s\n", stage.ID, stage.State)
		state.StageState[stage.ID] = stage.State
	}
}

// progressTerminalErr returns the non-nil error FollowJob's contract
// requires for a terminal failure/cancellation/timeout, mirroring
// FollowJobWithServiceToWriterResult's status-string branch.
func progressTerminalErr(p *DeploymentProgress) error {
	switch p.State {
	case "failed":
		return fmt.Errorf("job failed")
	case "cancelled":
		return fmt.Errorf("job cancelled")
	case "timed_out":
		return fmt.Errorf("job timed out")
	default:
		return nil
	}
}

// FollowDeploymentProgressToWriterResult polls GET
// /jobs/:jobId/deployment-progress exclusively until the job reaches a
// terminal state. It never calls a general job, work-item, raw-log,
// application-log, or account/cluster job-list endpoint, and it never
// falls back on ANY error, including the first read -- a 404 included
// (story 346, acceptance criterion 6, S4: a scoped refusal is final,
// no fallback, ever). Every native follow entry point (`runos deploy
// --follow`, VCS deploy `--follow`, `runos follow`, `runos jobs
// follow`) calls this directly once the caller has DECLARED scoped
// mode (--scoped or RUNOS_SCOPED=1): the CLI never probes this
// endpoint to detect scoped-ness on its own behalf. Default mode uses
// the general job follow (FollowJobWithServiceToWriter and friends)
// instead, never this function.
func FollowDeploymentProgressToWriterResult(ctx context.Context, svc *Service, jobID string, w io.Writer) (*DeploymentProgress, error) {
	first, err := svc.GetDeploymentProgress(jobID)
	if err != nil {
		return nil, err
	}
	return followProgressFrom(ctx, svc, jobID, w, first)
}

// followProgressFrom runs the progress poll loop starting from an
// already-fetched snapshot, so FollowDeploymentProgressToWriterResult
// doesn't need a second code path for "first read" vs "later poll".
// Any error from a later poll -- after the first read already
// admitted the caller -- is returned immediately with no retry: a
// mid-flight revocation or a stage-recheck refusal is final, never
// silently retried through a broader route (story 346, S4).
func followProgressFrom(ctx context.Context, svc *Service, jobID string, w io.Writer, first *DeploymentProgress) (*DeploymentProgress, error) {
	state := NewProgressFollowState()
	last := first
	emitProgressDelta(w, last, state)

	for !last.IsTerminal() {
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("job follow cancelled: %w", ctx.Err())
		default:
		}
		time.Sleep(pollInterval)

		p, err := svc.GetDeploymentProgress(jobID)
		if err != nil {
			return last, err
		}
		last = p
		emitProgressDelta(w, p, state)
	}
	return last, progressTerminalErr(last)
}

// ProgressToJobStatus maps a DeploymentProgress snapshot into the
// JobStatus shape (id/type/status/progress/currentStep/error) that
// `runos follow --json` / `runos jobs follow --json` have always
// published, so switching the underlying read to the sanitized
// endpoint keeps the same KEY SET a CI consumer parses (story 346,
// finding 1351). Two values are not preserved exactly, named here and
// in the CHANGELOG rather than left for a consumer to discover
// (finding 1409):
//
//   - Type is the literal constant "deploy", not a real job type. The
//     sanitized runos.deployment-progress.v1 contract carries no job
//     type field at all (it does not even distinguish a CLI upload
//     from a VCS deploy), so there is no real value to forward; a
//     general-route-served follow's Type instead comes verbatim from
//     Conductor.
//   - Status can be "timed_out", a DeploymentProgress state the
//     general route's JobStatus never published (JobStatus.IsTerminal
//     does not recognise it). A consumer that switches on `.status`
//     sees this one new member only for a progress-served follow.
//
// The mapping is also lossy in shape, not only value: there is no
// work-item list, no per-item log, no teardown block, because the
// sanitized endpoint carries none of that by design.
func ProgressToJobStatus(p *DeploymentProgress) *JobStatus {
	js := &JobStatus{
		ID:     p.JobID,
		Type:   "deploy",
		Status: p.State,
	}
	if n := len(p.Stages); n > 0 {
		last := p.Stages[n-1]
		js.CurrentStep = last.ID
		js.Progress = last.Message
	}
	if p.State == "failed" || p.State == "cancelled" || p.State == "timed_out" {
		js.Error = fmt.Sprintf("deployment %s", p.State)
	}
	return js
}
