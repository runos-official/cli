package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
// falls back on ANY error, including the first read: callers that need
// the documented 404-is-ambiguous exception use
// FollowAnyJobToWriterResult instead. This is the strict primitive a
// caller reaches for only once it already knows -- from its own
// dispatch, not from this call's result -- that the job must have a
// scoped binding.
func FollowDeploymentProgressToWriterResult(ctx context.Context, svc *Service, jobID string, w io.Writer) (*DeploymentProgress, error) {
	first, err := svc.GetDeploymentProgress(jobID)
	if err != nil {
		return nil, err
	}
	return followProgressFrom(ctx, svc, jobID, w, first)
}

// followProgressFrom runs the progress poll loop starting from an
// already-fetched snapshot, so a caller that had to probe the
// endpoint first (FollowAnyJobToWriterResult) doesn't re-request the
// same snapshot twice. Any error from a later poll -- after the first
// read already admitted the caller -- is returned immediately with no
// retry: a mid-flight revocation or a stage-recheck refusal is final,
// never silently retried through a broader route (story 346, D4).
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

// FollowResult carries whichever response shape a follow actually
// used. ProgressToJobStatus maps Progress into the stable JobStatus
// document shape a caller's own --json contract already promises
// (story 346, finding 1351), so callers do not need to branch on which
// field is set merely to produce stable output.
type FollowResult struct {
	// Progress is set when the sanitized deployment-progress endpoint
	// served the whole follow.
	Progress *DeploymentProgress
	// Job is set when the general job/work-item/log follow served the
	// whole follow (progress refused the very first read with the one
	// documented ambiguous code, 404).
	Job *JobStatus
}

// FollowAnyJobToWriterResult is the one fallback-aware follow
// dispatcher in this package. It backs every native follow entry
// point story 346 names: the standalone `runos follow` / `runos jobs
// follow <jobId>` commands (which take only a job id, with no context
// about how the job was created), AND `runos deploy --follow` /
// VCS deploy `--follow` (cmd/deploy.go, cmd/deploy_vcs.go), which know
// they just dispatched the job but must still tolerate a LEGACY or
// human-authored job: those never carry a scoped binding and the
// progress endpoint answers them with the same 404 "not found" it
// uses for a genuine stranger (handbook article
// conductor-api/jobs/get-deployment-progress's Refusals section).
//
// It tries the sanitized deployment-progress endpoint first, so a
// scoped machine key following its own deploy job never needs broader
// job-read authority. The fallback to the general job/work-item/log
// follow fires ONLY when that very first read fails with a
// *ProgressError whose StatusCode is 404 -- the one status the
// endpoint article documents as indistinguishable from "not a scoped
// deploy job". Every other first-read failure (403, 503, a transport
// error, a decode error) is a definitive refusal and is returned
// immediately: D4 requires that "a scoped refusal ... is final: no
// retry on a legacy or broader route", and nothing about a 403, a 503,
// or a broken connection is ambiguous the way a 404 is (finding 1325,
// 1350). A failure on a LATER poll, after progress already admitted
// the caller, is likewise never retried through the general route
// (see followProgressFrom): that admission makes the job's scoped
// status certain, so there is no residual ambiguity left to resolve
// by falling back.
func FollowAnyJobToWriterResult(ctx context.Context, svc *Service, jobID string, w io.Writer) (*FollowResult, error) {
	first, err := svc.GetDeploymentProgress(jobID)
	if err == nil {
		p, ferr := followProgressFrom(ctx, svc, jobID, w, first)
		return &FollowResult{Progress: p}, ferr
	}

	var progressErr *ProgressError
	if !errors.As(err, &progressErr) || progressErr.StatusCode != http.StatusNotFound {
		return nil, err
	}

	job, jerr := FollowJobWithServiceToWriterResult(ctx, svc, jobID, w)
	return &FollowResult{Job: job}, jerr
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
