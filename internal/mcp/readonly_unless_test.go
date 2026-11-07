package mcp

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func probeManifestCommand(rule *manifest.ReadOnlyUnless) manifest.Command {
	return manifest.Command{
		Command:        "maintenance-scripts/watchdog-probe/run",
		Method:         "POST",
		MCP:            []string{"read"},
		Description:    "Report whether each node has a hardware watchdog.",
		ReadOnlyUnless: rule,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "LOAD_DRIVER", Type: "string", Default: "false"},
			{Name: "NODE_NID", Type: "string"},
		}},
	}
}

// FCR 1909. The probe changes nothing by default, so MCP must not demand confirm=true for it.
// LOAD_DRIVER=true loads a kernel module and keeps the confirm. A marker the CLI cannot read
// keeps the confirm too.
func TestRefuseUnconfirmedDestructive_ReadOnlyUnless(t *testing.T) {
	probe := probeManifestCommand(&manifest.ReadOnlyUnless{Field: "LOAD_DRIVER", Values: []string{"true"}})
	if err := refuseUnconfirmedDestructive(probe, map[string]any{}); err != nil {
		t.Errorf("default probe must run without confirm, got %v", err)
	}
	if err := refuseUnconfirmedDestructive(probe, map[string]any{"LOAD_DRIVER": "false"}); err != nil {
		t.Errorf("LOAD_DRIVER=false must run without confirm, got %v", err)
	}
	if err := refuseUnconfirmedDestructive(probe, map[string]any{"LOAD_DRIVER": "true"}); err == nil {
		t.Error("LOAD_DRIVER=true without confirm must be refused")
	}
	if err := refuseUnconfirmedDestructive(probe, map[string]any{"LOAD_DRIVER": true}); err == nil {
		t.Error("LOAD_DRIVER bool true without confirm must be refused")
	}
	if err := refuseUnconfirmedDestructive(probe, map[string]any{"LOAD_DRIVER": "true", confirmArgName: true}); err != nil {
		t.Errorf("LOAD_DRIVER=true with confirm must run, got %v", err)
	}
	malformed := probeManifestCommand(&manifest.ReadOnlyUnless{Malformed: true})
	if err := refuseUnconfirmedDestructive(malformed, map[string]any{}); err == nil {
		t.Error("a malformed marker must keep the confirm")
	}
	apt := manifest.Command{Command: "maintenance-scripts/node-apt-upgrade-reboot/run", Method: "POST", MCP: []string{"write"}}
	if err := refuseUnconfirmedDestructive(apt, map[string]any{}); err == nil {
		t.Error("an unmarked maintenance script must still demand confirm")
	}
}

// The tool schema must not make confirm required for a call that is read-only by default, and
// the description must say when confirm is needed.
func TestBuildTools_ReadOnlyUnlessMakesConfirmConditional(t *testing.T) {
	m := &manifest.Manifest{Commands: []manifest.Command{
		probeManifestCommand(&manifest.ReadOnlyUnless{Field: "LOAD_DRIVER", Values: []string{"true"}}),
	}}
	srv := &Server{manifest: m, executor: &mockExecutor{}, category: "read"}
	tool := mustFindTool(t, srv.buildTools(), "maintenance-scripts_watchdog-probe_run")
	if countIn(tool.InputSchema.Required, confirmArgName) != 0 {
		t.Errorf("confirm must not be required for a read-only-by-default call, got %v", tool.InputSchema.Required)
	}
	if _, ok := tool.InputSchema.Properties[confirmArgName]; !ok {
		t.Error("confirm must stay in the schema for the LOAD_DRIVER=true case")
	}
	if !strings.Contains(tool.Description, "LOAD_DRIVER") || !strings.Contains(tool.Description, "confirm=true") {
		t.Errorf("description must name the field that needs confirm=true, got: %s", tool.Description)
	}
	if strings.Contains(tool.Description, "DESTRUCTIVE: this cannot be undone") {
		t.Errorf("the unconditional destructive notice must not apply, got: %s", tool.Description)
	}

	m2 := &manifest.Manifest{Commands: []manifest.Command{probeManifestCommand(&manifest.ReadOnlyUnless{Malformed: true})}}
	srv2 := &Server{manifest: m2, executor: &mockExecutor{}, category: "read"}
	tool2 := mustFindTool(t, srv2.buildTools(), "maintenance-scripts_watchdog-probe_run")
	if countIn(tool2.InputSchema.Required, confirmArgName) != 1 {
		t.Errorf("a malformed marker must keep confirm required, got %v", tool2.InputSchema.Required)
	}
}
