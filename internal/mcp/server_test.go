package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestBootstrapGate_ReadServerAllowsToolsAfterBootstrapAndTopics(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: "cluster data"})
	srv.bootstrapped = true
	srv.topicsRead = map[string]struct{}{
		"deploying-apps":       {},
		"dockerfile-templates": {},
		"runos-yaml":           {},
	}

	resp := srv.handleToolsCall(makeToolCallRequest("clusters_list"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if result.IsError {
		t.Fatalf("expected IsError to be false, got: %s", result.Content[0].Text)
	}
	if result.Content[0].Text != "cluster data" {
		t.Errorf("unexpected result: %s", result.Content[0].Text)
	}
}

func TestTopicsGate_ReadServerBlocksUntilThreeTopicsRead(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: "cluster data"})
	srv.bootstrapped = true
	srv.topicsRead = map[string]struct{}{
		"deploying-apps": {},
		"runos-yaml":     {},
	}

	resp := srv.handleToolsCall(makeToolCallRequest("clusters_list"))

	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if !result.IsError {
		t.Fatal("expected IsError to be true when fewer than 3 topics have been read")
	}
	if !strings.Contains(result.Content[0].Text, "2/3") {
		t.Errorf("expected error to report 2/3 topics read, got: %s", result.Content[0].Text)
	}
}

func TestTopicsGate_TopicsShowAlwaysAllowedAndRecordsKey(t *testing.T) {
	srv := newTestServer("read", &mockExecutor{result: `{"key":"deploying-apps","title":"Deploying","content":"..."}`})
	srv.bootstrapped = true

	params, _ := json.Marshal(CallToolParams{
		Name:      "mcp_topics_show",
		Arguments: map[string]any{"key": "deploying-apps"},
	})
	req := &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params}

	resp := srv.handleToolsCall(req)
	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if result.IsError {
		t.Fatalf("expected mcp_topics_show to succeed, got: %s", result.Content[0].Text)
	}
	if _, recorded := srv.topicsRead["deploying-apps"]; !recorded {
		t.Fatalf("expected deploying-apps to be recorded, topicsRead=%v", srv.topicsRead)
	}
}

func TestTopicsGate_TopicsSearchExtractsKeysFromResponse(t *testing.T) {
	searchResult := `{"topics":[{"key":"deploying-apps","title":"Deploying"},{"key":"dockerfile-templates","title":"Dockerfile"},{"key":"runos-yaml","title":"YAML"}],"count":3}`
	srv := newTestServer("read", &mockExecutor{result: searchResult})
	srv.bootstrapped = true

	params, _ := json.Marshal(CallToolParams{
		Name:      "mcp_topics_search",
		Arguments: map[string]any{"keywords": "deploy"},
	})
	req := &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params}

	resp := srv.handleToolsCall(req)
	result, ok := resp.Result.(CallToolResult)
	if !ok {
		t.Fatal("expected CallToolResult")
	}
	if result.IsError {
		t.Fatalf("expected mcp_topics_search to succeed, got: %s", result.Content[0].Text)
	}
	for _, want := range []string{"deploying-apps", "dockerfile-templates", "runos-yaml"} {
		if _, ok := srv.topicsRead[want]; !ok {
			t.Errorf("expected %q to be recorded, topicsRead=%v", want, srv.topicsRead)
		}
	}
	if len(srv.topicsRead) != 3 {
		t.Errorf("expected 3 topics recorded, got %d", len(srv.topicsRead))
	}
}

func TestTopicsGate_NonReadServerSkipsTopicsGate(t *testing.T) {
	for _, category := range []string{"sensitive_read", "write", "sensitive_write"} {
		t.Run(category, func(t *testing.T) {
			srv := newTestServer(category, &mockExecutor{result: "ok"})

			resp := srv.handleToolsCall(makeToolCallRequest("some_tool"))

			result, ok := resp.Result.(CallToolResult)
			if !ok {
				t.Fatal("expected CallToolResult")
			}
			if result.IsError {
				t.Fatalf("expected no error for %s server with no topics read, got: %s", category, result.Content[0].Text)
			}
		})
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

// Regression target for I6-H: array fields with itemType="object" +
// itemFields must project to a JSON Schema `items` shape that names the
// object's properties (not the legacy `items.type=string` fallback), so
// MCP clients that strict-validate the tool schema send the right
// element shape to conductor instead of stringly-typed values that
// trigger a 400 on the apps/secret-files/update.add path.
func TestProjectArrayItems(t *testing.T) {
	s := &Server{}

	t.Run("no itemType falls back to string", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{Name: "remove", Type: "array"})
		if got == nil || got.Type != "string" {
			t.Fatalf("legacy []string field: got %+v, want {Type: string}", got)
		}
		if got.Properties != nil || got.Required != nil {
			t.Errorf("legacy field should have nil Properties/Required, got %+v", got)
		}
	})

	t.Run("itemType string emits scalar items", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{
			Name:     "remove",
			Type:     "array",
			ItemType: "string",
		})
		if got == nil || got.Type != "string" {
			t.Fatalf("got %+v, want Type=string", got)
		}
	})

	t.Run("itemType object with itemFields emits properties + required", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{
			Name:     "add",
			Type:     "array",
			ItemType: "object",
			ItemFields: []manifest.Field{
				{Name: "filename", Type: "string", Description: "key in the Secret", Required: true},
				{Name: "mountPath", Type: "string", Required: true},
				{Name: "content", Type: "string", Description: "base64", Required: true},
				{Name: "note", Type: "string", Description: "optional"},
			},
		})
		if got == nil || got.Type != "object" {
			t.Fatalf("got %+v, want Type=object", got)
		}
		if len(got.Properties) != 4 {
			t.Errorf("want 4 properties, got %d: %+v", len(got.Properties), got.Properties)
		}
		if got.Properties["filename"].Type != "string" || got.Properties["filename"].Description != "key in the Secret" {
			t.Errorf("filename prop: %+v", got.Properties["filename"])
		}
		// Required is built in declaration order from the required sub-fields.
		wantRequired := []string{"filename", "mountPath", "content"}
		if len(got.Required) != len(wantRequired) {
			t.Fatalf("required len: got %v, want %v", got.Required, wantRequired)
		}
		for i, w := range wantRequired {
			if got.Required[i] != w {
				t.Errorf("required[%d] = %q, want %q", i, got.Required[i], w)
			}
		}
	})

	t.Run("itemType object without itemFields emits bare object", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{
			Name:     "providerOptions",
			Type:     "array",
			ItemType: "object",
		})
		if got == nil || got.Type != "object" {
			t.Fatalf("got %+v, want Type=object", got)
		}
		if got.Properties != nil || got.Required != nil {
			t.Errorf("bare object items should have nil Properties/Required, got %+v", got)
		}
	})

	t.Run("itemType integer maps through mapType", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{
			Name:     "ports",
			Type:     "array",
			ItemType: "integer",
		})
		if got == nil || got.Type != "number" {
			t.Fatalf("integer maps to number per mapType; got %+v", got)
		}
	})

	t.Run("required sub-field with enum + default", func(t *testing.T) {
		got := s.projectArrayItems(manifest.Field{
			Name:     "rules",
			Type:     "array",
			ItemType: "object",
			ItemFields: []manifest.Field{
				{Name: "method", Type: "string", Enum: []string{"GET", "POST"}, Default: "GET", Required: true},
			},
		})
		methodProp := got.Properties["method"]
		if methodProp.Type != "string" {
			t.Errorf("method type: %s", methodProp.Type)
		}
		if len(methodProp.Enum) != 2 || methodProp.Enum[0] != "GET" {
			t.Errorf("method enum: %v", methodProp.Enum)
		}
		if methodProp.Default != "GET" {
			t.Errorf("method default: %v", methodProp.Default)
		}
	})
}

// TestCheckStaleBinary pins the I13-A detection: the server records
// the running binary's mtime at startup, and checkStaleBinary flips
// sticky-true once the on-disk mtime advances past that. Three cases
// matter: clean (mtime unchanged), rebuilt (mtime advanced), and
// missing-baseline (startupBinaryPath was never captured — checkStaleBinary
// must NOT trip in that case, since we can't tell).
func TestCheckStaleBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-runos")
	if err := os.WriteFile(binPath, []byte("v1"), 0o755); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	info, err := os.Stat(binPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	startMtime := info.ModTime()

	t.Run("clean (no rebuild)", func(t *testing.T) {
		s := &Server{startupBinaryPath: binPath, startupBinaryMtime: startMtime}
		if s.checkStaleBinary() {
			t.Error("expected false on clean check, got true")
		}
		if s.staleBinaryDetected {
			t.Error("sticky flag should not be set on clean check")
		}
	})

	t.Run("rebuilt (mtime advanced)", func(t *testing.T) {
		s := &Server{startupBinaryPath: binPath, startupBinaryMtime: startMtime}
		future := startMtime.Add(2 * time.Second)
		if err := os.Chtimes(binPath, future, future); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if !s.checkStaleBinary() {
			t.Error("expected true after rebuild, got false")
		}
		if !s.staleBinaryDetected {
			t.Error("sticky flag should be set")
		}
		// Second call returns true even if mtime walks backwards (sticky).
		if err := os.Chtimes(binPath, startMtime, startMtime); err != nil {
			t.Fatalf("chtimes revert: %v", err)
		}
		if !s.checkStaleBinary() {
			t.Error("sticky flag must not reset on mtime regression")
		}
	})

	t.Run("missing-baseline (path never captured)", func(t *testing.T) {
		s := &Server{} // startupBinaryPath empty, mtime zero
		if s.checkStaleBinary() {
			t.Error("expected false when baseline missing")
		}
	})

	t.Run("stat error (binary deleted)", func(t *testing.T) {
		gonePath := filepath.Join(dir, "gone-runos")
		s := &Server{startupBinaryPath: gonePath, startupBinaryMtime: startMtime}
		if s.checkStaleBinary() {
			t.Error("expected false when stat fails")
		}
	})
}

// TestProjectObjectValue pins the I26-N fix: MCP tool schemas now emit
// structured `additionalProperties` for object-typed map fields, not
// the legacy `{type: "string"}` that forced clients to stringify their
// payload.
func TestProjectObjectValue(t *testing.T) {
	s := &Server{}

	t.Run("providerOptions drops constraint entirely (existing carve-out)", func(t *testing.T) {
		got := s.projectObjectValue(manifest.Field{Name: "providerOptions", Type: "object"})
		if got != nil {
			t.Errorf("providerOptions should return nil to drop the constraint, got %+v", got)
		}
	})

	t.Run("manifest valueType=object + valueFields wins", func(t *testing.T) {
		got := s.projectObjectValue(manifest.Field{
			Name:      "custom",
			Type:      "object",
			ValueType: "object",
			ValueFields: []manifest.Field{
				{Name: "id", Type: "string", Required: true},
				{Name: "weight", Type: "integer"},
			},
		})
		if got == nil || got.Type != "object" {
			t.Fatalf("expected object value schema, got %+v", got)
		}
		if got.Properties["id"].Type != "string" {
			t.Errorf("id property type = %q, want string", got.Properties["id"].Type)
		}
		if got.Properties["weight"].Type != "number" {
			t.Errorf("weight (integer→number) type = %q, want number", got.Properties["weight"].Type)
		}
		if len(got.Required) != 1 || got.Required[0] != "id" {
			t.Errorf("Required = %v, want [id]", got.Required)
		}
	})

	t.Run("requires falls back to hardcoded shape when manifest doesn't declare valueType", func(t *testing.T) {
		got := s.projectObjectValue(manifest.Field{Name: "requires", Type: "object"})
		if got == nil || got.Type != "object" {
			t.Fatalf("expected object schema for requires, got %+v", got)
		}
		for _, want := range []string{"id", "type", "config", "env"} {
			if _, ok := got.Properties[want]; !ok {
				t.Errorf("requires schema missing property %q", want)
			}
		}
		// id + type required; config + env optional.
		gotRequired := map[string]bool{}
		for _, r := range got.Required {
			gotRequired[r] = true
		}
		if !gotRequired["id"] || !gotRequired["type"] {
			t.Errorf("Required should include id + type, got %v", got.Required)
		}
		if gotRequired["config"] || gotRequired["env"] {
			t.Errorf("Required should NOT include config/env, got %v", got.Required)
		}
		// config + env are themselves maps of string→string.
		if got.Properties["config"].AdditionalProperties == nil || got.Properties["config"].AdditionalProperties.Type != "string" {
			t.Errorf("config.additionalProperties = %+v, want string", got.Properties["config"].AdditionalProperties)
		}
	})

	t.Run("manifest valueType wins over the requires hardcode", func(t *testing.T) {
		// If the conductor manifest later declares the shape on the
		// `requires` field directly, the manifest-driven branch takes
		// over and the hardcoded fallback drops out.
		got := s.projectObjectValue(manifest.Field{
			Name:      "requires",
			Type:      "object",
			ValueType: "string", // hypothetical future "stringly typed requires"
		})
		if got == nil || got.Type != "string" {
			t.Errorf("manifest valueType should win, got %+v", got)
		}
	})

	t.Run("default falls back to string for unknown object fields", func(t *testing.T) {
		got := s.projectObjectValue(manifest.Field{Name: "tagIds", Type: "object"})
		if got == nil || got.Type != "string" {
			t.Errorf("unknown object field should default to string, got %+v", got)
		}
	})
}

// foreman #78 regression: ContentBlock.Text must NOT carry an
// `omitempty` JSON tag. The MCP spec requires every text content
// block to include a `text` field, and conformant clients reject
// `{"type":"text"}` with an invalid_union error. Empty success
// frames must marshal to `{"type":"text","text":""}`.
func TestContentBlockMarshalsTextEvenWhenEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   ContentBlock
		want string
	}{
		{
			name: "non-empty text",
			in:   ContentBlock{Type: "text", Text: "API key revoked"},
			want: `{"type":"text","text":"API key revoked"}`,
		},
		{
			name: "empty text must still emit text field",
			in:   ContentBlock{Type: "text", Text: ""},
			want: `{"type":"text","text":""}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("ContentBlock JSON = %s, want %s", got, c.want)
			}
			if !strings.Contains(string(got), `"text"`) {
				t.Errorf("text field omitted from %s; MCP spec requires it", got)
			}
		})
	}
}
