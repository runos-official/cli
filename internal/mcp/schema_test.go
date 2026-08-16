package mcp

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func mustFindTool(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	tool := findTool(tools, name)
	if tool == nil {
		t.Fatalf("tool %q not built", name)
	}
	return *tool
}

func countIn(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

// Review 2 item 3. The injected `cid` property was appended to Required
// unconditionally, so every command that ALSO declares cid as a required
// input field shipped `"required":["cid","cid"]`. A duplicate entry is
// invalid JSON Schema for strict validators and reads as two arguments.
func TestBuildTools_RequiredHasNoDuplicateCID(t *testing.T) {
	m := &manifest.Manifest{Commands: []manifest.Command{{
		Command:  "vms/show",
		Endpoint: "/:aid/:cid/vms/:vmid",
		Method:   "GET",
		MCP:      []string{"read"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "cid", Type: "string", Required: true, Positional: true},
			{Name: "vmid", Type: "string", Required: true, Positional: true},
		}},
	}}}
	srv := &Server{manifest: m, executor: &mockExecutor{}, category: "read"}

	tool := mustFindTool(t, srv.buildTools(), "vms_show")

	if got := countIn(tool.InputSchema.Required, "cid"); got != 1 {
		t.Fatalf("cid must appear once in required, got %d in %v", got, tool.InputSchema.Required)
	}
	if got := countIn(tool.InputSchema.Required, "vmid"); got != 1 {
		t.Errorf("vmid must appear once in required, got %d in %v", got, tool.InputSchema.Required)
	}
}

// The same dedupe protects `confirm`: a manifest that grows a confirm
// field must not double it against the injected one.
func TestBuildTools_RequiredHasNoDuplicateConfirm(t *testing.T) {
	m := &manifest.Manifest{Commands: []manifest.Command{{
		Command: "vms/delete",
		Method:  "DELETE",
		MCP:     []string{"write"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "confirm", Type: "boolean", Required: true},
		}},
	}}}
	srv := &Server{manifest: m, executor: &mockExecutor{}, category: "write"}

	tool := mustFindTool(t, srv.buildTools(), "vms_delete")

	if got := countIn(tool.InputSchema.Required, "confirm"); got != 1 {
		t.Fatalf("confirm must appear once in required, got %d in %v", got, tool.InputSchema.Required)
	}
}

// Review 2 item 6. The tool DESCRIPTION is what an LLM reads before it
// composes a call. A destructive tool that demands confirm=true has to
// say so there, not only in the property description it may never open.
func TestBuildTools_DestructiveDescriptionDemandsConfirm(t *testing.T) {
	m := &manifest.Manifest{Commands: []manifest.Command{
		{Command: "clusters/etcd-remove-member", Method: "PATCH", MCP: []string{"write"}, Description: "Remove one member from the etcd quorum."},
		{Command: "vms/list", Method: "GET", MCP: []string{"write"}, Description: "List the virtual machines."},
	}}
	srv := &Server{manifest: m, executor: &mockExecutor{}, category: "write"}
	tools := srv.buildTools()

	destructive := mustFindTool(t, tools, "clusters_etcd-remove-member")
	if !strings.Contains(destructive.Description, "confirm=true") {
		t.Errorf("a destructive tool description must state confirm=true, got: %s", destructive.Description)
	}
	if !strings.Contains(destructive.Description, "Remove one member") {
		t.Errorf("the manifest description must be kept, got: %s", destructive.Description)
	}

	safe := mustFindTool(t, tools, "vms_list")
	if strings.Contains(safe.Description, "confirm=true") {
		t.Errorf("a read tool must not be told to confirm, got: %s", safe.Description)
	}
}
