package mcp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// mockExecutor implements ToolExecutor for testing
type mockExecutor struct {
	result string
	err    error
}

func (m *mockExecutor) Execute(toolName string, args map[string]any) (string, error) {
	return m.result, m.err
}

func (m *mockExecutor) ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error) {
	return m.result, m.err
}

func makeToolCallRequest(name string) *Request {
	params, _ := json.Marshal(CallToolParams{Name: name})
	return &Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  params,
	}
}

func newTestServer(category string, executor ToolExecutor) *Server {
	return &Server{
		manifest: &manifest.Manifest{},
		executor: executor,
		version:  "test",
		category: category,
	}
}

func TestBootstrapGate_ReadServerBlocksBeforeBootstrap(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: "ok"})

	resp := srv.handleToolsCall(makeToolCallRequest("clusters_list"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if !result.IsError {
		t.Fatal("expected IsError to be true")
	}
	if result.Content[0].Text == "" || result.Content[0].Text != "ERROR: You must call the mcp_bootstrap tool before using any other tools. Call mcp_bootstrap now (no arguments needed) to receive critical instructions for correct RunOS usage." {
		t.Errorf("unexpected error message: %s", result.Content[0].Text)
	}
}

func TestBootstrapGate_ReadServerAllowsBootstrap(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: "bootstrap docs here"})

	resp := srv.handleToolsCall(makeToolCallRequest("mcp_bootstrap"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if result.IsError {
		t.Fatal("expected IsError to be false")
	}
	if result.Content[0].Text != "bootstrap docs here" {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}
	if !srv.bootstrapped {
		t.Fatal("expected bootstrapped to be true after successful mcp_bootstrap call")
	}
}

func TestBootstrapGate_ReadServerAllowsToolsAfterBootstrap(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: "cluster data"})
	srv.bootstrapped = true

	resp := srv.handleToolsCall(makeToolCallRequest("clusters_list"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if result.IsError {
		t.Fatal("expected IsError to be false")
	}
	if result.Content[0].Text != "cluster data" {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}
}

func TestBootstrapGate_NonReadServerSkipsGuard(t *testing.T) {
	for _, category := range []string{"sensitive_read", "write", "sensitive_write"} {
		t.Run(category, func(t *testing.T) {
			srv := newTestServer(category, &mockExecutor{result: "ok"})

			resp := srv.handleToolsCall(makeToolCallRequest("some_tool"))

			result, ok := resp.Result.(CallToolResult)
			if !ok {
				t.Fatal("expected CallToolResult")
			}
			if result.IsError {
				t.Fatalf("expected no error for %s server before bootstrap", category)
			}
		})
	}
}

func TestBootstrapGate_BootstrapFailureDoesNotSetFlag(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{err: fmt.Errorf("auth failed")})

	resp := srv.handleToolsCall(makeToolCallRequest("mcp_bootstrap"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if !result.IsError {
		t.Fatal("expected IsError to be true on bootstrap failure")
	}
	if srv.bootstrapped {
		t.Fatal("expected bootstrapped to remain false after failed mcp_bootstrap call")
	}
}
