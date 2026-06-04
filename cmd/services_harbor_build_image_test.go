package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/jobs"
)

// TestHarborBuildImageJSONResponseShape pins the --json envelope contract:
// fire-and-forget carries jobId/repo/tags/images and omits the two result
// fields; --follow populates durationMs + skippedBecauseCached (as *bool so
// an explicit false is distinguishable from absent).
func TestHarborBuildImageJSONResponseShape(t *testing.T) {
	// Fire-and-forget: no result fields, no error.
	queued := harborBuildImageJSONResponse{
		JobID:  "job123",
		Repo:   "acme-vm-workspace",
		Tags:   []string{"latest"},
		Images: []string{"runos-apps/acme-vm-workspace:latest"},
	}
	out, err := json.Marshal(queued)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	for _, want := range []string{`"jobId":"job123"`, `"repo":"acme-vm-workspace"`, `"images":["runos-apps/acme-vm-workspace:latest"]`} {
		if !strings.Contains(s, want) {
			t.Errorf("queued envelope missing %s; got %s", want, s)
		}
	}
	for _, absent := range []string{"skippedBecauseCached", "durationMs", "error"} {
		if strings.Contains(s, absent) {
			t.Errorf("queued envelope should omit %q; got %s", absent, s)
		}
	}

	// Followed to completion: result fields populated, explicit false present.
	cached := false
	done := harborBuildImageJSONResponse{
		JobID:                "job123",
		Repo:                 "acme-vm-workspace",
		Tags:                 []string{"latest"},
		Images:               []string{"runos-apps/acme-vm-workspace:latest"},
		SkippedBecauseCached: &cached,
		DurationMs:           4200,
	}
	out, err = json.Marshal(done)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s = string(out)
	for _, want := range []string{`"skippedBecauseCached":false`, `"durationMs":4200`} {
		if !strings.Contains(s, want) {
			t.Errorf("done envelope missing %s; got %s", want, s)
		}
	}
}

// TestHarborBuildImageSummary pins the confirmation block: fixed runos-apps
// project, repo, comma-joined tags, context, and cluster.
func TestHarborBuildImageSummary(t *testing.T) {
	got := harborBuildImageSummary("acme-vm-workspace", "/src/docker/vm-workspace", "mycluster", []string{"latest", "v1"})
	for _, want := range []string{
		"runos-apps/acme-vm-workspace",
		"latest, v1",
		"/src/docker/vm-workspace",
		"mycluster",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q; got:\n%s", want, got)
		}
	}
}

// TestSummarizeHarborBuildImage pins the terminal-state phrasing: with a
// duration it reports "Pushed <refs> in <d>."; without, just "Pushed
// <refs>."; with no images, a generic completion line.
func TestSummarizeHarborBuildImage(t *testing.T) {
	images := []string{"runos-apps/tool:latest"}

	var b strings.Builder
	summarizeHarborBuildImage(&b, images, &jobs.BuildResult{DurationMs: 5000})
	if !strings.Contains(b.String(), "Pushed runos-apps/tool:latest in 5s.") {
		t.Errorf("expected duration phrasing; got %q", b.String())
	}

	b.Reset()
	summarizeHarborBuildImage(&b, images, nil)
	if !strings.Contains(b.String(), "Pushed runos-apps/tool:latest.") {
		t.Errorf("expected no-duration phrasing; got %q", b.String())
	}

	b.Reset()
	summarizeHarborBuildImage(&b, nil, nil)
	if !strings.Contains(b.String(), "Build complete.") {
		t.Errorf("expected generic completion; got %q", b.String())
	}
}
