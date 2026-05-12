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
