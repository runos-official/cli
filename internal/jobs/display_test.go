package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEmitFollowDeltas_FirstPollEmitsAll(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{ID: "abc", Status: "running", Progress: "1/3"}
	items := []WorkItem{
		{ID: "i1", Name: "prepare", Status: "completed", StepNumber: 1},
		{ID: "i2", Name: "upload", Status: "in_progress", StepNumber: 2},
	}

	var buf bytes.Buffer
	EmitFollowDeltas(&buf, job, items, state)
	out := buf.String()

	for _, want := range []string{
		"job abc: running (1/3)",
		"step 1 prepare: completed",
		"step 2 upload: in_progress",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestEmitFollowDeltas_SecondPollEmitsOnlyDeltas(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{ID: "abc", Status: "running", Progress: "1/3"}
	items := []WorkItem{
		{ID: "i1", Name: "prepare", Status: "completed", StepNumber: 1},
		{ID: "i2", Name: "upload", Status: "in_progress", StepNumber: 2},
	}
	// Prime: first poll emits everything, drop the output.
	var primed bytes.Buffer
	EmitFollowDeltas(&primed, job, items, state)

	// Second poll: nothing changed → no output.
	var second bytes.Buffer
	EmitFollowDeltas(&second, job, items, state)
	if strings.TrimSpace(second.String()) != "" {
		t.Errorf("expected no output on no-change poll, got:\n%s", second.String())
	}

	// Third poll: upload completes, job advances to 2/3.
	job.Progress = "2/3"
	items[1].Status = "completed"
	items[1].RawResult = json.RawMessage(`"uploaded 12MB"`)
	var third bytes.Buffer
	EmitFollowDeltas(&third, job, items, state)
	out := third.String()
	if !strings.Contains(out, "job abc: running (2/3)") {
		t.Errorf("expected job progress line, got:\n%s", out)
	}
	if !strings.Contains(out, "step 2 upload: completed (uploaded 12MB)") {
		t.Errorf("expected step transition with result, got:\n%s", out)
	}
	if strings.Contains(out, "step 1 prepare") {
		t.Errorf("step 1 didn't transition, should not appear in delta output:\n%s", out)
	}
}

func TestEmitFollowDeltas_TerminalFailureCarriesError(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{
		ID:       "abc",
		Status:   "failed",
		Progress: "2/3",
		Error:    "build timeout after 10m",
	}
	var buf bytes.Buffer
	EmitFollowDeltas(&buf, job, nil, state)
	want := "job abc: failed (2/3): build timeout after 10m"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("expected output to contain %q, got:\n%s", want, buf.String())
	}
}

func TestEmitFollowDeltasRendersCurrentTeardownOutcome(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{
		ID:       "55555555-5555-4555-8555-555555555555",
		Status:   "completed",
		Progress: "4/4",
		RawBody: json.RawMessage(`{
			"id":"55555555-5555-4555-8555-555555555555",
			"status":"completed",
			"teardowns":[{
				"id":"11111111-1111-4111-8111-111111111111",
				"aid":"22222222-2222-4222-8222-222222222222",
				"cid":"33333333-3333-4333-8333-333333333333",
				"nid":"44444444-4444-4444-8444-444444444444",
				"name":"",
				"jobId":"55555555-5555-4555-8555-555555555555",
				"operationKind":"cluster_reset",
				"acknowledgementApplicable":true,
				"state":"pending",
				"acceptanceState":"accepted",
				"reasonCode":"",
				"reason":"",
				"remedy":"",
				"requestedAt":"2026-09-09T12:00:00Z",
				"dispatchedAt":null,
				"resolvedAt":null,
				"updatedAt":"2026-09-09T12:00:00Z",
				"trackingDeadlineAt":"2026-09-09T12:01:00Z",
				"providerState":"not_requested",
				"providerReason":"",
				"providerRemedy":""
			}],
			"teardownRead":{"aid":"22222222-2222-4222-8222-222222222222","cid":"33333333-3333-4333-8333-333333333333","jobId":"55555555-5555-4555-8555-555555555555"},
			"teardownNextCursor":null,
			"teardownReadError":null
		}`),
	}

	var buffer bytes.Buffer
	EmitFollowDeltas(&buffer, job, nil, state)
	rendered := buffer.String()
	for _, expected := range []string{
		"job 55555555-5555-4555-8555-555555555555: bookkeeping completed",
		"Agent acknowledgement: Scheduling acknowledgement remains pending.",
		"runos node-teardowns show 11111111-1111-4111-8111-111111111111",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("follow output missing %q:\n%s", expected, rendered)
		}
	}
}

func TestEmitFollowDeltasRendersLaterRecoveryGuidance(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{
		ID:       "55555555-5555-4555-8555-555555555555",
		Status:   "running",
		Progress: "3/4",
		RawBody:  acknowledgedTeardownJob(""),
	}

	var first bytes.Buffer
	EmitFollowDeltas(&first, job, nil, state)
	if !strings.Contains(first.String(), "acknowledged scheduling uninstall") {
		t.Fatalf("first outcome missing acknowledgement:\n%s", first.String())
	}

	job.RawBody = acknowledgedTeardownJob("Check the surviving machine before another operation.")
	var second bytes.Buffer
	EmitFollowDeltas(&second, job, nil, state)
	if !strings.Contains(second.String(), "Check the surviving machine before another operation.") {
		t.Fatalf("later recovery guidance was suppressed:\n%s", second.String())
	}
	if strings.Contains(second.String(), "job 55555555") {
		t.Fatalf("unchanged job status was repeated:\n%s", second.String())
	}
}

func acknowledgedTeardownJob(remedy string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"id":"55555555-5555-4555-8555-555555555555",
		"status":"running",
		"teardowns":[{
			"id":"11111111-1111-4111-8111-111111111111",
			"aid":"22222222-2222-4222-8222-222222222222",
			"cid":"33333333-3333-4333-8333-333333333333",
			"nid":"44444444-4444-4444-8444-444444444444",
			"name":"target",
			"jobId":"55555555-5555-4555-8555-555555555555",
			"operationKind":"cluster_reset",
			"acknowledgementApplicable":true,
			"state":"acknowledged",
			"acceptanceState":"accepted",
			"reasonCode":"",
			"reason":"",
			"remedy":%q,
			"requestedAt":"2026-09-09T12:00:00Z",
			"dispatchedAt":"2026-09-09T12:00:01Z",
			"resolvedAt":"2026-09-09T12:00:02Z",
			"updatedAt":"2026-09-09T12:00:03Z",
			"trackingDeadlineAt":"2026-09-09T12:01:00Z",
			"providerState":"not_requested",
			"providerReason":"",
			"providerRemedy":""
		}]
	}`, remedy))
}

func TestEmitFollowDeltas_NoEscapeCodes(t *testing.T) {
	t.Parallel()
	state := NewFollowState()
	job := &JobStatus{ID: "abc", Status: "running", Progress: "1/1"}
	items := []WorkItem{{ID: "i1", Name: "x", Status: "running", StepNumber: 1}}
	var buf bytes.Buffer
	EmitFollowDeltas(&buf, job, items, state)
	if strings.ContainsRune(buf.String(), '\x1b') {
		t.Errorf("output must not contain terminal escape codes, got:\n%q", buf.String())
	}
}

// Regression test for I2-2a (TEST_LOG.md): the live deploy transcript
// previously emitted all work-item status lines first, then all log
// lines, producing an output like:
//
//	step 6 Wait for rollout: completed
//	step 7 Record service dependencies: completed
//	step 8 Reconcile custom domains: running
//	  Watching rollout for app-...           <-- step 6 log
//	  Recording 1 service dependencies        <-- step 7 log
//
// Causal ordering was lost: step 6's progress logs landed underneath
// step 7's "completed" header. EmitFollowDeltasWithLogs interleaves
// per item: each step's status line is followed immediately by its
// own new log lines, then the next step.
func TestEmitFollowDeltasWithLogs_PerItemInterleaving(t *testing.T) {
	t.Parallel()
	logsByItem := map[string][]string{
		"i6": {"Watching rollout for app-x"},
		"i7": {"Recording 1 service dependencies"},
		"i8": {"Domains already in sync"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path: /jobs/<jobID>/workitems/<workItemID>/logs
		parts := strings.Split(r.URL.Path, "/")
		var workItemID string
		for i, p := range parts {
			if p == "workitems" && i+1 < len(parts) {
				workItemID = parts[i+1]
				break
			}
		}
		entries := logsByItem[workItemID]
		logs := make([]WorkItemLog, len(entries))
		for i, m := range entries {
			logs[i] = WorkItemLog{ID: fmt.Sprintf("log-%d", i), WorkItemID: workItemID, Message: m}
		}
		_ = json.NewEncoder(w).Encode(WorkItemLogsResponse{WorkItemID: workItemID, Logs: logs})
	}))
	t.Cleanup(srv.Close)

	svc := &Service{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		token:      "t",
	}

	state := NewFollowState()
	job := &JobStatus{ID: "j", Status: "running", Progress: "6/8"}
	items := []WorkItem{
		{ID: "i6", Name: "Wait for rollout", Status: "completed", StepNumber: 6},
		{ID: "i7", Name: "Record service dependencies", Status: "completed", StepNumber: 7},
		{ID: "i8", Name: "Reconcile custom domains", Status: "running", StepNumber: 8},
	}

	var buf bytes.Buffer
	EmitFollowDeltasWithLogs(&buf, svc, "j", job, items, state)
	out := buf.String()

	idx := func(s string) int { return strings.Index(out, s) }
	step6Header := idx("step 6 Wait for rollout: completed")
	step6Log := idx("Watching rollout for app-x")
	step7Header := idx("step 7 Record service dependencies: completed")
	step7Log := idx("Recording 1 service dependencies")
	step8Header := idx("step 8 Reconcile custom domains: running")
	step8Log := idx("Domains already in sync")

	for name, val := range map[string]int{
		"step 6 header": step6Header,
		"step 6 log":    step6Log,
		"step 7 header": step7Header,
		"step 7 log":    step7Log,
		"step 8 header": step8Header,
		"step 8 log":    step8Log,
	} {
		if val < 0 {
			t.Fatalf("%s missing from output:\n%s", name, out)
		}
	}

	// Causal-order assertions: each step's log lands AFTER its own
	// header AND BEFORE the next step's header.
	if step6Log < step6Header {
		t.Errorf("step 6 log appeared before its header:\n%s", out)
	}
	if step6Log > step7Header {
		t.Errorf("step 6 log appeared after step 7 header (I2-2a regression):\n%s", out)
	}
	if step7Log < step7Header || step7Log > step8Header {
		t.Errorf("step 7 log not bracketed between step 7 and step 8 headers:\n%s", out)
	}
	if step8Log < step8Header {
		t.Errorf("step 8 log appeared before its header:\n%s", out)
	}
}

func TestTruncResult(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"  hello  ", "hello"},
		{"first line\nsecond line", "first line"},
		{strings.Repeat("a", 100), strings.Repeat("a", 77) + "..."},
	}
	for _, c := range cases {
		if got := truncResult(c.in); got != c.want {
			t.Errorf("truncResult(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
