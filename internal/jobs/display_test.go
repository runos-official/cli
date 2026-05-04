package jobs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
