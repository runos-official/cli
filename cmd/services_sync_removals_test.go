package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/services"
)

// Objective 102 / story 263, criterion 5. A removal is the one part of a
// sync body an operator cannot read off the yaml, because the evidence
// for it is a line that is NO LONGER in the file. In the unified diff it
// is one deletion among the other edits, and in the body it is an empty
// value that looks like any other value. So the plan names each removal
// on its own line, in words that say the field is removed rather than
// changed, before the confirmation prompt.
//
// Every id, name and tag below is a placeholder.

func TestServicesSyncPlanNamesEachRemovalBeforeTheConfirmation(t *testing.T) {
	plan := &services.SyncPlan{
		Type: "postgresql",
		ID:   "abc12",
		CID:  "cluster1",
		PatchBody: map[string]any{
			"replicas":         1,
			"nodeAffinityTags": []any{},
			"placementNote":    "",
		},
		Removals: []string{"nodeAffinityTags", "placementNote"},
	}

	out := captureStdout(t, func() {
		printServicesSyncPlan(plan, false)
	})

	if !strings.Contains(out, "removed") {
		t.Fatalf("the plan has no removed section:\n%s", out)
	}
	for _, name := range plan.Removals {
		line := findLine(out, name+": removed")
		if line == "" {
			t.Errorf("no line names %q as removed:\n%s", name, out)
			continue
		}
		// The wording has to be unambiguous about direction. A reader
		// who skims "nodeAffinityTags: []" in a body can read it as a
		// value; "removed" cannot be read that way.
		if !strings.Contains(line, "deleted from the yaml") {
			t.Errorf("the removal line for %q does not say why it is being removed: %q", name, line)
		}
	}

	// One line per field, not a single comma-joined line.
	if got := strings.Count(out, ": removed"); got != len(plan.Removals) {
		t.Errorf("counted %d removal lines, want %d:\n%s", got, len(plan.Removals), out)
	}
}

// A plan that removes nothing prints no removed section, so the section
// is a signal rather than furniture.
func TestServicesSyncPlanPrintsNoRemovedSectionWhenNothingIsRemoved(t *testing.T) {
	plan := &services.SyncPlan{
		Type:      "postgresql",
		ID:        "abc12",
		CID:       "cluster1",
		PatchBody: map[string]any{"replicas": 1},
	}
	out := captureStdout(t, func() {
		printServicesSyncPlan(plan, false)
	})
	if strings.Contains(out, "removed") {
		t.Errorf("a plan with no removal printed a removed section:\n%s", out)
	}
}

// findLine returns the first line containing substr, or "".
func findLine(out, substr string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}
