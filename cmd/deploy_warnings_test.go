package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// Objective 92 / story 211, criterion 7. Story 211 taught every
// manifest-driven command to render conductor's `warnings` array. The
// static deploy path had that behaviour already, and must keep printing
// exactly one line per entry and no more: `deploy` is not manifest-driven
// (cmd/root.go hands deployCmd to the dynamic builder as an existing
// command, so the manifest's `deploy` entry merges into this static
// command instead of growing a dynacmd leaf), so nothing can double-print.
func TestPrintPrepareWarnings(t *testing.T) {
	t.Run("two warnings print two lines and no more", func(t *testing.T) {
		var buf bytes.Buffer
		printPrepareWarnings(&buf, []string{"first advisory", "second advisory"})

		want := "Warning: first advisory\nWarning: second advisory\n"
		if buf.String() != want {
			t.Errorf("output = %q, want %q", buf.String(), want)
		}
		if got := strings.Count(buf.String(), "Warning:"); got != 2 {
			t.Errorf("counted %d warning lines, want exactly 2", got)
		}
	})

	t.Run("no warnings writes nothing", func(t *testing.T) {
		var buf bytes.Buffer
		printPrepareWarnings(&buf, nil)
		if buf.Len() != 0 {
			t.Errorf("output = %q, want nothing", buf.String())
		}
	})
}

// The prepare response is where the array arrives. Pinned so a wire
// rename cannot silently empty the loop above.
func TestPrepareResponseDecodesWarnings(t *testing.T) {
	var resp deploy.PrepareResponse
	body := `{"jobId":"job-1","warnings":["first advisory","second advisory"]}`
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Warnings) != 2 || resp.Warnings[0] != "first advisory" {
		t.Errorf("Warnings = %#v, want the two advisories in order", resp.Warnings)
	}
}
