package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// scriptedExecutor fails the named tools and answers every other tool
// with result. The bootstrap-gate tests need a bootstrap that fails while
// the rest of the surface still works, which is exactly the shape of the
// defect: an expired sign-in refuses mcp_bootstrap and leaves the whole
// server refusing everything else with a message about bootstrap.
type scriptedExecutor struct {
	result  string
	failing map[string]error
}

func (s *scriptedExecutor) Execute(toolName string, args map[string]any) (string, error) {
	if err, ok := s.failing[toolName]; ok {
		return "", err
	}
	return s.result, nil
}

func (s *scriptedExecutor) ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error) {
	return s.result, nil
}

func toolCallResult(t *testing.T, resp *Response) CallToolResult {
	t.Helper()
	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatalf("expected CallToolResult, got %#v", resp.Result)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected at least one content block")
	}
	return result
}

// Review 2 item 2. The bootstrap gate reached the write servers (B16) and
// manifest_update sat behind it, so a bootstrap that FAILS (expired token,
// conductor down) made the write servers unusable and left no way to
// refresh a stale command list. manifest_update is local work that needs
// no bootstrap, so it runs whatever the gate says.
func TestBootstrapGate_ManifestUpdateIsExempt(t *testing.T) {
	for _, category := range []string{"read", "write", "sensitive_write"} {
		t.Run(category, func(t *testing.T) {
			live := &manifest.Manifest{Version: "40.1.0"}
			srv := &Server{manifest: live, executor: &mockExecutor{result: "ok"}, category: category}
			srv.SetManifestReloader(&fakeReloader{
				serverVersion: "40.7.0",
				updated:       &manifest.Manifest{Version: "40.7.0"},
			})

			result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("manifest_update")))

			if result.IsError {
				t.Fatalf("manifest_update must run before bootstrap, got: %s", result.Content[0].Text)
			}
			if !strings.Contains(result.Content[0].Text, "40.7.0") {
				t.Errorf("expected the refreshed version in the result, got: %s", result.Content[0].Text)
			}
		})
	}
}

// Review 2 item 2. A gate nobody can open is a lockout. Once a bootstrap
// attempt has FAILED, the gate stops refusing and prepends a warning
// instead, so the caller can still act and still knows the instructions
// were never read.
func TestBootstrapGate_DowngradesToAWarningAfterAFailedAttempt(t *testing.T) {
	exec := &scriptedExecutor{
		result:  "cluster data",
		failing: map[string]error{"mcp_bootstrap": fmt.Errorf("401 Unauthorized: token expired")},
	}
	srv := newTestServer("write", exec)

	bootstrap := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("mcp_bootstrap")))
	if !bootstrap.IsError {
		t.Fatal("expected the failed bootstrap to be reported as an error")
	}

	result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("clusters_list")))
	if result.IsError {
		t.Fatalf("a failed bootstrap must not make the write server unusable, got: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "cluster data") {
		t.Errorf("expected the tool result to be returned, got: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "mcp_bootstrap") {
		t.Errorf("expected a warning naming mcp_bootstrap, got: %s", result.Content[0].Text)
	}
}

// The refusal stays for the caller that never tried. Only a FAILED
// attempt downgrades the gate.
func TestBootstrapGate_StillRefusesWhenNoAttemptWasMade(t *testing.T) {
	srv := newTestServer("write", &mockExecutor{result: "ok"})

	result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("clusters_list")))

	if !result.IsError {
		t.Fatal("expected the bootstrap gate to refuse a caller that never called mcp_bootstrap")
	}
	if !strings.Contains(result.Content[0].Text, "must call the mcp_bootstrap tool") {
		t.Errorf("unexpected refusal text: %s", result.Content[0].Text)
	}
}

// Review 2 item 18. The topic gate counted every key a SEARCH returned,
// so one search that happened to match two topics opened the gate without
// the agent reading a single topic body. Only mcp_topics_show counts now,
// with mcp_bootstrap still counting as exactly one.
func TestTopicsGate_SearchDoesNotCountAsAReadTopic(t *testing.T) {
	searchResult := `{"topics":[{"key":"deploying-apps"},{"key":"runos-yaml"},{"key":"vm-storage"}],"count":3}`
	srv := newTestServer("read", &mockExecutor{result: searchResult})
	srv.bootstrapped = true

	params, _ := json.Marshal(CallToolParams{
		Name:      "mcp_topics_search",
		Arguments: map[string]any{"keywords": "deploy"},
	})
	srv.handleToolsCall(&Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})

	if len(srv.topicsRead) != 0 {
		t.Fatalf("a search result must not count as a read topic, got %v", srv.topicsRead)
	}

	result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("clusters_list")))
	if !result.IsError {
		t.Fatal("expected the topic gate to stay shut after a search alone")
	}
}

// mcp_bootstrap counts as ONE documentation read. This asserts that a topic
// show is still RECORDED (the counter works), not that one is required.
func TestTopicsGate_BootstrapPlusOneShowOpensTheGate(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: `{"key":"vm-storage","content":"..."}`})

	srv.handleToolsCall(makeToolCallRequest("mcp_bootstrap"))

	params, _ := json.Marshal(CallToolParams{
		Name:      "mcp_topics_show",
		Arguments: map[string]any{"key": "vm-storage"},
	})
	srv.handleToolsCall(&Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})

	if len(srv.topicsRead) != 2 {
		t.Fatalf("expected bootstrap and the topic show both recorded, got %v", srv.topicsRead)
	}
	result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest("clusters_list")))
	if result.IsError {
		t.Fatalf("expected the gate open after bootstrap plus one topic read, got: %s", result.Content[0].Text)
	}
}
