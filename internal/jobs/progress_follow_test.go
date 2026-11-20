package jobs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// pathLog is the follow half of story 346 criterion 6's dispatch
// matrix (the prepare/upload/VCS half lives in
// internal/deploy/dispatch_matrix_test.go): it proves that following a
// deploy job through the sanitized progress endpoint never reaches a
// general job, work-item, raw-log, or account/cluster job-list route,
// and that a scoped refusal from the progress endpoint is final, with
// zero such legacy requests, no retry.
type pathLog struct {
	mu    sync.Mutex
	paths []string
}

func (p *pathLog) record(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, path)
}

func (p *pathLog) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, got := range p.paths {
		if got == path {
			n++
		}
	}
	return n
}

func (p *pathLog) any(pred func(string) bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, got := range p.paths {
		if pred(got) {
			return true
		}
	}
	return false
}

const testJobID = "55555555-5555-4555-8555-555555555555"

func isLegacyJobRoute(path string) bool {
	if path == "/jobs/"+testJobID {
		return true
	}
	if path == "/jobs/"+testJobID+"/workitems" {
		return true
	}
	if strings.Contains(path, "/workitems/") && strings.Contains(path, "/logs") {
		return true
	}
	if strings.HasSuffix(path, "/jobs") && path != "/jobs/"+testJobID {
		return true
	}
	return false
}

// TestFollowDeploymentProgressToWriterResult_UsesProgressEndpointOnly
// covers the "capability open" half of criterion 6: once admitted, a
// deploy-only credential follows its own job through the job-only
// endpoint alone, and the request log contains no broad preparation,
// job, or log request.
func TestFollowDeploymentProgressToWriterResult_UsesProgressEndpointOnly(t *testing.T) {
	log := &pathLog{}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		if r.URL.Path != "/jobs/"+testJobID+"/deployment-progress" {
			http.NotFound(w, r)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		state := "running"
		if calls >= 2 {
			state = "completed"
		}
		w.Write([]byte(`{"version":"runos.deployment-progress.v1","jobId":"` + testJobID + `","appId":"app01","state":"` + state + `","startedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:01Z","stages":[{"id":"source_fetch","state":"` + state + `","message":"` + state + `"}]}`))
	}))
	t.Cleanup(srv.Close)

	orig := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = orig })

	svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
	ctx := context.Background()
	var out bytes.Buffer
	_, err := FollowDeploymentProgressToWriterResult(ctx, svc, testJobID, &out)
	if err != nil {
		t.Fatalf("FollowDeploymentProgressToWriterResult: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected at least 2 progress polls to observe the running->completed transition, got %d", calls)
	}
	if log.any(isLegacyJobRoute) {
		t.Errorf("follow reached a general job/work-item/log route; paths = %v", log.paths)
	}
	if !strings.Contains(out.String(), "completed") {
		t.Errorf("output %q missing terminal completed state", out.String())
	}
}

// TestFollowDeploymentProgressToWriterResult_ScopedRefusalIsFinal
// covers the "capability closed" half of criterion 6: EVERY refusal
// from the progress endpoint -- 404 included -- is final in scoped
// mode, with exactly one request and zero legacy fallback requests.
// Story 346 S4 deliberately drops the old general-route's "404 is the
// one documented ambiguous status" exception: that exception existed
// only because the CLI used to have to guess whether a job was
// scoped. Scoped mode is now DECLARED by the caller, so a 404 here
// means exactly what it says (not found / no scoped binding) and gets
// no more benefit of the doubt than a 403 or a 503. The 404 row is
// the one a reintroduced fallback (the deleted FollowAnyJobToWriterResult
// mechanism) would silently pass by retrying through the general
// route instead of surfacing this refusal.
func TestFollowDeploymentProgressToWriterResult_ScopedRefusalIsFinal(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"404 not found / no scoped binding", http.StatusNotFound},
		{"403 forbidden", http.StatusForbidden},
		{"503 stage-recheck capability still closed", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &pathLog{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.record(r.URL.Path)
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":"refused"}`))
			}))
			t.Cleanup(srv.Close)

			svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
			var out bytes.Buffer
			_, err := FollowDeploymentProgressToWriterResult(context.Background(), svc, testJobID, &out)
			if err == nil {
				t.Fatal("expected a refusal error, got nil")
			}
			var progressErr *ProgressError
			if !errors.As(err, &progressErr) {
				t.Fatalf("error = %v, want a *ProgressError", err)
			}
			if progressErr.StatusCode != tc.status {
				t.Errorf("status = %d, want %d", progressErr.StatusCode, tc.status)
			}
			if got := len(log.paths); got != 1 {
				t.Errorf("request count = %d, want exactly 1 (no retry, no fallback)", got)
			}
			if log.any(isLegacyJobRoute) {
				t.Errorf("a scoped refusal must never fall back to a legacy route; paths = %v", log.paths)
			}
		})
	}
}

// TestProgressToJobStatus is the colocated unit test finding 1409
// asks for: a table over every published DeploymentProgress state
// (running, completed, failed, cancelled, timed_out), asserting the
// exact JobStatus fields the mapping produces. A mutation that dropped
// ID or Status, or that stopped setting Error on a terminal failure
// state, would fail one of these rows.
func TestProgressToJobStatus(t *testing.T) {
	cases := []struct {
		name       string
		progress   *DeploymentProgress
		wantStatus string
		wantError  string
		wantStep   string
		wantProg   string
	}{
		{
			name:       "running, no stages yet",
			progress:   &DeploymentProgress{JobID: testJobID, State: "running"},
			wantStatus: "running",
		},
		{
			name: "running with a stage",
			progress: &DeploymentProgress{JobID: testJobID, State: "running", Stages: []ProgressStage{
				{ID: "source_fetch", State: "completed", Message: "completed"},
				{ID: "build_start", State: "running", Message: "started"},
			}},
			wantStatus: "running",
			wantStep:   "build_start",
			wantProg:   "started",
		},
		{
			name:       "completed",
			progress:   &DeploymentProgress{JobID: testJobID, State: "completed"},
			wantStatus: "completed",
		},
		{
			name:       "failed sets Error",
			progress:   &DeploymentProgress{JobID: testJobID, State: "failed"},
			wantStatus: "failed",
			wantError:  "deployment failed",
		},
		{
			name:       "cancelled sets Error",
			progress:   &DeploymentProgress{JobID: testJobID, State: "cancelled"},
			wantStatus: "cancelled",
			wantError:  "deployment cancelled",
		},
		{
			name:       "timed_out sets Error (a state JobStatus never otherwise publishes)",
			progress:   &DeploymentProgress{JobID: testJobID, State: "timed_out"},
			wantStatus: "timed_out",
			wantError:  "deployment timed_out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProgressToJobStatus(tc.progress)
			if got.ID != testJobID {
				t.Errorf("ID = %q, want %q", got.ID, testJobID)
			}
			if got.Type != "deploy" {
				t.Errorf("Type = %q, want the synthesized constant %q", got.Type, "deploy")
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Error != tc.wantError {
				t.Errorf("Error = %q, want %q", got.Error, tc.wantError)
			}
			if got.CurrentStep != tc.wantStep {
				t.Errorf("CurrentStep = %q, want %q", got.CurrentStep, tc.wantStep)
			}
			if got.Progress != tc.wantProg {
				t.Errorf("Progress = %q, want %q", got.Progress, tc.wantProg)
			}
		})
	}
}
