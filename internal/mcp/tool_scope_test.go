package mcp

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

type tierTestExecutor struct {
	mockExecutor
	calls []string
}

func (e *tierTestExecutor) Execute(name string, args map[string]any) (string, error) {
	e.calls = append(e.calls, name)
	return e.mockExecutor.Execute(name, args)
}

func TestToolCallChecksServerTierBeforeDispatch(t *testing.T) {
	commands := []manifest.Command{
		{Command: "services/valkey/{id}/show", MCP: []string{"read"}},
		{Command: "services/valkey/{id}/delete", MCP: []string{"write"}},
		{Command: "services/minio/{id}/credentials", MCP: []string{"sensitive_read"}},
		{Command: "account/modules/{key}/enable", MCP: []string{"write"}},
		{Command: "run", MCP: []string{"read"}},
	}
	cases := []struct {
		tier string
		name string
		want bool
	}{
		{"read", "services_valkey_show", true},
		{"read", "services_valkey_delete", false},
		{"read", "services_minio_credentials", false},
		{"read", "account_modules_enable", false},
		{"read", "run", false},
		{"read", "services/valkey/show", false},
		{"sensitive_read", "services_minio_credentials", true},
		{"write", "services_valkey_delete", true},
		{"write", "services_valkey_show", false},
	}
	for _, tc := range cases {
		t.Run(tc.tier+"/"+tc.name, func(t *testing.T) {
			exec := &tierTestExecutor{mockExecutor: mockExecutor{result: "ok"}}
			srv := newTestServer(tc.tier, exec)
			srv.manifest.Commands = commands
			srv.bootstrapped = true
			srv.topicsRead = map[string]struct{}{"first": {}, "second": {}, "third": {}}

			resp := srv.handleToolsCall(makeToolCallRequest(tc.name))
			result, ok := resp.Result.(CallToolResult)
			if !ok || result.IsError == tc.want {
				t.Fatalf("unexpected result: %+v", resp)
			}
			if !tc.want {
				if len(exec.calls) != 0 || !strings.Contains(result.Content[0].Text, "unavailable in the "+tc.tier+" tier") {
					t.Fatalf("the tier must refuse before dispatch: calls=%v result=%+v", exec.calls, result)
				}
				return
			}
			if len(exec.calls) != 1 || exec.calls[0] != tc.name {
				t.Fatalf("calls = %v, want [%s]", exec.calls, tc.name)
			}
		})
	}
}

func TestToolDiscoveryCannotAdvertiseABlockedDispatch(t *testing.T) {
	srv := newTestServer("read", &tierTestExecutor{})
	srv.manifest.Commands = []manifest.Command{
		{Command: "services/example/{id}/credentials", MCP: []string{"sensitive_read"}},
		{Command: "services/example/credentials", MCP: []string{"read"}},
		{Command: "run", MCP: []string{"read"}},
		{Command: "deploy/{id}", MCP: []string{"read"}},
		{Command: "deploy", MCP: []string{"read", "sensitive_write"}},
	}

	for _, tool := range srv.buildTools() {
		if !srv.toolAllowedInTier(tool.Name) {
			t.Errorf("advertised tool %q is unavailable to its own server", tool.Name)
		}
	}
	if srv.toolAllowedInTier("services_example_credentials") || srv.toolAllowedInTier("run") {
		t.Fatal("a colliding manifest entry must not widen the dispatch tier")
	}
	if srv.toolAllowedInTier("deploy") {
		t.Fatal("a manifest entry cannot move the deploy subprocess to the read tier")
	}
	srv.category = "sensitive_write"
	if !srv.toolAllowedInTier("deploy") {
		t.Fatal("the exact deploy manifest entry must remain available in sensitive_write")
	}
}

func TestToolCallReservesExactDocumentationCommands(t *testing.T) {
	cases := []struct {
		path  string
		alias string
	}{
		{"mcp/bootstrap", "mcp_bootstrap"},
		{"mcp/topics/search", "mcp/topics/{id}/search"},
		{"mcp/topics/show", "mcp_topics_show"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			exec := &tierTestExecutor{mockExecutor: mockExecutor{result: "ok"}}
			srv := newTestServer("read", exec)
			srv.bootstrapped = true
			srv.manifest.Commands = []manifest.Command{{Command: tc.alias, MCP: []string{"sensitive_write"}}}
			name := manifestToolName(tc.path)

			result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest(name)))
			if !result.IsError || len(exec.calls) != 0 {
				t.Fatalf("a reserved alias reached the executor: calls=%v result=%+v", exec.calls, result)
			}

			srv.manifest.Commands = append(srv.manifest.Commands, manifest.Command{Command: tc.path})
			cmd := manifestCommandForTool(srv.manifest, name)
			if cmd == nil || cmd.Command != tc.path {
				t.Fatalf("resolved %v, want exact path %s", cmd, tc.path)
			}
			result = toolCallResult(t, srv.handleToolsCall(makeToolCallRequest(name)))
			if result.IsError || len(exec.calls) != 1 {
				t.Fatalf("the exact documentation tool must remain available: calls=%v result=%+v", exec.calls, result)
			}
		})
	}
}
