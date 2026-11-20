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

// TestFollowDeploymentJobToWriter_UsesProgressEndpointOnly covers the
// "capability open" half of criterion 6: once admitted, a deploy-only
// credential follows its own job through the job-only endpoint alone,
// and the request log contains no broad preparation, job, or log
// request.
func TestFollowDeploymentJobToWriter_UsesProgressEndpointOnly(t *testing.T) {
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

// TestFollowDeploymentJobToWriter_ScopedRefusalIsFinal covers the
// "capability closed" half of criterion 6: a 503 from the progress
// endpoint (the stage-recheck capability still closed, or any other
// scoped denial) is refused immediately, with exactly one request and
// zero legacy fallback requests -- never a retry through a broader
// route, never advice to use another credential.
func TestFollowDeploymentJobToWriter_ScopedRefusalIsFinal(t *testing.T) {
	log := &pathLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"deploy.upload_token_unavailable"}`))
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
	if progressErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", progressErr.StatusCode)
	}
	if got := len(log.paths); got != 1 {
		t.Errorf("request count = %d, want exactly 1 (no retry)", got)
	}
	if log.any(isLegacyJobRoute) {
		t.Errorf("a scoped refusal must never fall back to a legacy route; paths = %v", log.paths)
	}
}

// TestFollowAnyJobToWriterResult_FallsBackOnlyOnFirstRefusal exercises
// the standalone `runos follow` entry point's documented exception: a
// job the progress endpoint refuses on its VERY FIRST read (no scoped
// binding -- legacy, human, or non-deploy job) falls back to the
// general job follow, which has its own independent authorization.
func TestFollowAnyJobToWriterResult_FallsBackOnlyOnFirstRefusal(t *testing.T) {
	log := &pathLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/jobs/"+testJobID+"/deployment-progress":
			http.NotFound(w, r) // no scoped binding: legacy job
		case r.URL.Path == "/jobs/"+testJobID:
			w.Write([]byte(`{"id":"` + testJobID + `","status":"completed"}`))
		case r.URL.Path == "/jobs/"+testJobID+"/workitems":
			w.Write([]byte(`{"jobId":"` + testJobID + `","workItems":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
	var out bytes.Buffer
	result, err := FollowAnyJobToWriterResult(context.Background(), svc, testJobID, &out)
	if err != nil {
		t.Fatalf("FollowAnyJobToWriterResult: %v", err)
	}
	if result.Job == nil || result.Progress != nil {
		t.Fatalf("expected a Job-shaped fallback result, got %#v", result)
	}
	if got := log.count("/jobs/" + testJobID + "/deployment-progress"); got != 1 {
		t.Errorf("progress probe count = %d, want exactly 1 (no retry of the refused endpoint)", got)
	}
}

// TestFollowAnyJobToWriterResult_NeverFallsBackAfterProgressAdmits
// proves the standalone follow command's fallback is a FIRST-READ-ONLY
// exception: once the progress endpoint has admitted the caller for
// this job, a LATER refusal (revocation, expiry, or a stage-recheck
// denial) is final and is never retried through the general route.
func TestFollowAnyJobToWriterResult_NeverFallsBackAfterProgressAdmits(t *testing.T) {
	log := &pathLog{}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		if r.URL.Path != "/jobs/"+testJobID+"/deployment-progress" {
			http.NotFound(w, r)
			return
		}
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"version":"runos.deployment-progress.v1","jobId":"` + testJobID + `","appId":"app01","state":"running","startedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"revoked"}`))
	}))
	t.Cleanup(srv.Close)

	orig := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = orig })

	svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
	var out bytes.Buffer
	_, err := FollowAnyJobToWriterResult(context.Background(), svc, testJobID, &out)
	if err == nil {
		t.Fatal("expected the second-call 403 to surface as an error")
	}
	if log.any(isLegacyJobRoute) {
		t.Errorf("a mid-follow scoped refusal must never fall back to a legacy route; paths = %v", log.paths)
	}
}
