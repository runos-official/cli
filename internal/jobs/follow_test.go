package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJobsConductor stands up an httptest.Server returning the JobStatus
// states from `statuses` in order on successive GET /jobs/:id calls. The
// last entry is held forever once consumed (so the poll loop doesn't run
// off the end). GET /jobs/:id/workitems returns an empty list — WaitForJob
// doesn't read work-items, but the FollowJobWithService streaming path
// would.
//
// Used by V12 regression tests for WaitForJob's terminal-status contract:
// pre-fix, apps_sync prints "ok" the moment the conductor accepts the
// API call; this helper makes the conductor return "running" then
// "completed" / "failed" so we can assert WaitForJob blocks correctly.
func fakeJobsConductor(t *testing.T, statuses []JobStatus) *httptest.Server {
	t.Helper()
	var idx int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/workitems"):
			writeJSONForFollowTest(t, w, WorkItemsResponse{WorkItems: []WorkItem{}})
		case strings.Contains(r.URL.Path, "/workitems/") && strings.HasSuffix(r.URL.Path, "/logs"):
			writeJSONForFollowTest(t, w, WorkItemLogsResponse{Logs: []WorkItemLog{}})
		default:
			i := atomic.AddInt32(&idx, 1) - 1
			if int(i) >= len(statuses) {
				i = int32(len(statuses) - 1)
			}
			writeJSONForFollowTest(t, w, statuses[i])
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeJSONForFollowTest(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

// Regression test for V12 (VCS_DEPLOY_TEST_NOTES.md): WaitForJob blocks
// the caller until the conductor reports a terminal status. Pre-fix,
// apps_sync exited 0 the moment the API accepted the request and never
// observed the underlying job's success or failure. This helper is the
// silent-poll counterpart to FollowJobWithService and is what apps_sync
// dispatches into in non-TTY mode.
func TestWaitForJob_CompletedTerminalReturnsNil(t *testing.T) {
	srv := fakeJobsConductor(t, []JobStatus{
		{ID: "j1", Status: "running", Progress: "0/2"},
		{ID: "j1", Status: "running", Progress: "1/2"},
		{ID: "j1", Status: "completed", Progress: "2/2"},
	})
	svc := newServiceForTest(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForJob(ctx, svc, "j1"); err != nil {
		t.Fatalf("WaitForJob: unexpected error %v", err)
	}
}

// Regression test for V12: WaitForJob surfaces the conductor's job error
// verbatim on a terminal "failed" status. The cluster-side `kubectl patch`
// failure on missing configmaps (zuqgb/nvcsw repro) lands as
// `Error from server (NotFound): configmaps "app-zuqgb-user-env-config" not found`
// in JobStatus.Error; the CLI must hand that string back to the caller
// so apps_sync can print it and exit non-zero.
func TestWaitForJob_FailedTerminalSurfacesError(t *testing.T) {
	srv := fakeJobsConductor(t, []JobStatus{
		{ID: "j2", Status: "running", Progress: "0/2"},
		{ID: "j2", Status: "failed", Progress: "1/2", Error: `Error from server (NotFound): configmaps "app-zuqgb-user-env-config" not found`},
	})
	svc := newServiceForTest(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := WaitForJob(ctx, svc, "j2")
	if err == nil {
		t.Fatal("expected error on terminal failed status")
	}
	if !strings.Contains(err.Error(), `configmaps "app-zuqgb-user-env-config" not found`) {
		t.Errorf("error message must include conductor's job.Error verbatim; got %q", err.Error())
	}
}
