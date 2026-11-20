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
// application-log, or account/cluster job-list endpoint: a scoped
// Account API Key or Personal Access Token that can start a deploy
// but holds no broader job-read authority must be able to follow its
// OWN job to completion through this endpoint alone (story 346,
// D3/D4). Any error -- on the first call or a later poll -- is
// returned immediately with no retry: a mid-flight revocation or a
// stage-recheck refusal is final, never silently retried through a
// broader route.
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
// same snapshot twice.
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

// FollowDeploymentJobToWriter is the entry point `runos deploy
// --follow` and VCS deploy `--follow` use (cmd/deploy.go,
// cmd/deploy_vcs.go). It builds its own Service and a 30-minute
// timeout, mirroring FollowJobToWriter, and follows jobID through the
// sanitized progress endpoint only -- see
// FollowDeploymentProgressToWriterResult.
func FollowDeploymentJobToWriter(jobID string, w io.Writer) error {
	svc, err := NewService()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	_, err = FollowDeploymentProgressToWriterResult(ctx, svc, jobID, w)
	return err
}

// FollowResult carries whichever response shape a standalone follow
// actually used, so a JSON caller can marshal the real terminal
// response instead of synthesizing one.
type FollowResult struct {
	// Progress is set when the sanitized deployment-progress endpoint
	// served the whole follow.
	Progress *DeploymentProgress
	// Job is set when the general job/work-item/log follow served the
	// whole follow (progress refused the very first read).
	Job *JobStatus
}

// FollowAnyJobToWriterResult backs the standalone `runos follow` /
// `runos jobs follow <jobId>` commands, which take only a job id with
// no context about how the job was created or what kind of job it is
// (story 346, D3). It tries the sanitized deployment-progress endpoint
// first, so a scoped machine key following its own deploy job never
// needs broader job-read authority. If that very FIRST read fails --
// the job predates scoped authority, isn't a deploy job, or its
// binding is legacy/absent, all of which the endpoint deliberately
// answers with the same refusal as "not found" -- it falls back to
// the general job/work-item/log follow, which carries its OWN
// independent authorization and refuses on its own terms for a caller
// that truly lacks access. A failure on a LATER poll, after progress
// already admitted the caller, is never retried through the general
// route (same rule as FollowDeploymentProgressToWriterResult): a
// definitive scoped refusal stays final.
func FollowAnyJobToWriterResult(ctx context.Context, svc *Service, jobID string, w io.Writer) (*FollowResult, error) {
	if first, err := svc.GetDeploymentProgress(jobID); err == nil {
		p, ferr := followProgressFrom(ctx, svc, jobID, w, first)
		return &FollowResult{Progress: p}, ferr
	}

	job, err := FollowJobWithServiceToWriterResult(ctx, svc, jobID, w)
	return &FollowResult{Job: job}, err
}
