package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// fakeModuleAPI is one conductor: it holds the enabled flag, the toggle
// tools flip it, and the manifest it serves follows.
//
// Stateful on purpose. A reloader that always answers "the module is on"
// makes the tools/list BEFORE the toggle refresh itself through the
// version re-check, and the test then proves nothing about the toggle.
type fakeModuleAPI struct {
	on bool
	// frozen keeps the served manifest at its starting shape however the
	// flag moves, which is the ordinary case of a module whose tools all
	// belong to another server's category.
	frozen bool
	// toggleErr refuses the toggle, leaving the flag alone.
	toggleErr error
	// updateErr lets the toggle succeed and the refetch fail.
	updateErr   error
	updateCalls int
}

func (f *fakeModuleAPI) Execute(toolName string, args map[string]any) (string, error) {
	if f.toggleErr != nil {
		return "", f.toggleErr
	}
	switch toolName {
	case moduleEnableToolName:
		f.on = true
		return "virt is on", nil
	case moduleDisableToolName:
		f.on = false
		return "virt is off", nil
	}
	return "ok", nil
}

func (f *fakeModuleAPI) ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error) {
	return "ok", nil
}

func (f *fakeModuleAPI) served() bool {
	if f.frozen {
		return !f.on
	}
	return f.on
}

func (f *fakeModuleAPI) ServerVersion() (string, error) {
	return moduleVersion(f.served()), nil
}

func (f *fakeModuleAPI) ForceUpdate() (*manifest.Manifest, error) {
	f.updateCalls++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return moduleManifest(f.served()), nil
}

// moduleServer wires one fakeModuleAPI as both the executor and the
// reloader, which is how the real server is wired.
func moduleServer(api *fakeModuleAPI) *Server {
	srv := &Server{manifest: moduleManifest(api.on), executor: api, category: "write", bootstrapped: true}
	srv.SetManifestReloader(api)
	return srv
}

// listedTools names what this server advertises right now, through a real
// tools/list call, because that is what a client actually reads.
func listedTools(t *testing.T, srv *Server) []string {
	t.Helper()
	resp := srv.handleToolsList(&Request{JSONRPC: "2.0", ID: 1, Method: "tools/list"})
	result, ok := resp.Result.(ToolsListResult)
	if !ok {
		t.Fatalf("tools/list returned %T", resp.Result)
	}
	return toolNames(result.Tools)
}

// moduleManifest is the shape FPL34 fixes: the two toggle commands are
// core, never module-tagged, and take the module key positionally.
func moduleManifest(withVMs bool) *manifest.Manifest {
	key := &manifest.Input{Fields: []manifest.Field{{Name: "key", Type: "string", Required: true, Positional: true}}}
	m := &manifest.Manifest{
		Version: "45.3.0",
		Commands: []manifest.Command{
			{Command: "account/modules/enable", Endpoint: "/:aid/modules/{key}/enable", Method: "POST", MCP: []string{"write"}, Input: key},
			{Command: "account/modules/disable", Endpoint: "/:aid/modules/{key}/disable", Method: "POST", MCP: []string{"write"}, Input: key},
		},
	}
	if withVMs {
		m.Version = "45.3.0+virt"
		m.Commands = append(m.Commands, manifest.Command{Command: "vms/list", Endpoint: "/:aid/vms", Method: "GET", MCP: []string{"write"}})
	}
	return m
}

// toggleRequest is a tools/call carrying the module key, which the
// endpoint needs and makeToolCallRequest does not supply.
func toggleRequest(toolName string) *Request {
	params, _ := json.Marshal(CallToolParams{Name: toolName, Arguments: map[string]any{"key": "virt"}})
	return &Request{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params}
}

// Criterion 10. Enabling a module has to widen the tool list in the same
// process: the agent that ran the toggle expects the VM tools on its next
// turn, and a restart is not something it can do.
func TestEnableAddsTheModuleToolsAndAnnouncesThem(t *testing.T) {
	srv := moduleServer(&fakeModuleAPI{on: false})

	if before := listedTools(t, srv); slices.Contains(before, "vms_list") {
		t.Fatalf("vms_list is listed before the module is on: %v", before)
	}

	out := captureStdout(t, func() {
		result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest(moduleEnableToolName)))
		if result.IsError {
			t.Errorf("a successful toggle must not be an error: %s", result.Content[0].Text)
		}
	})

	if !strings.Contains(out, "tools/list_changed") {
		t.Errorf("a widened tool list must be announced, got: %s", out)
	}
	if after := listedTools(t, srv); !slices.Contains(after, "vms_list") {
		t.Errorf("vms_list still missing after enable: %v", after)
	}
}

// Criterion 11. The mirror case: disabling shrinks the list and announces
// it, so a client stops offering tools the API would now refuse.
func TestDisableRemovesTheModuleToolsAndAnnouncesThem(t *testing.T) {
	srv := moduleServer(&fakeModuleAPI{on: true})

	if before := listedTools(t, srv); !slices.Contains(before, "vms_list") {
		t.Fatalf("vms_list must be listed before the disable: %v", before)
	}

	out := captureStdout(t, func() {
		srv.handleToolsCall(makeToolCallRequest(moduleDisableToolName))
	})

	if !strings.Contains(out, "tools/list_changed") {
		t.Errorf("a shrunken tool list must be announced, got: %s", out)
	}
	if after := listedTools(t, srv); slices.Contains(after, "vms_list") {
		t.Errorf("vms_list survived the disable: %v", after)
	}
}

// Criterion 12. A toggle that FAILED changed nothing, so refetching the
// command list would be a wasted round trip and announcing it would be a
// lie.
func TestAFailedToggleRefetchesNothing(t *testing.T) {
	api := &fakeModuleAPI{on: false, toggleErr: fmt.Errorf(`{"error":"forbidden","statusCode":403}`)}
	srv := moduleServer(api)
	api.updateCalls = 0 // the constructor's tools/list is not under test

	out := captureStdout(t, func() {
		result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest(moduleEnableToolName)))
		if !result.IsError {
			t.Error("a refused toggle must surface as an error")
		}
	})

	if strings.Contains(out, "tools/list_changed") {
		t.Errorf("a failed toggle announced a change: %s", out)
	}
	if api.updateCalls != 0 {
		t.Errorf("a failed toggle refetched the manifest %d times", api.updateCalls)
	}
}

// The rule manifest_update already follows (review 2 item 22): a toggle
// that changed no tool on THIS server emits nothing. A module whose tools
// all live on another category is the ordinary case.
func TestAToggleThatChangedNoToolEmitsNothing(t *testing.T) {
	srv := moduleServer(&fakeModuleAPI{on: false, frozen: true})

	out := captureStdout(t, func() {
		srv.handleToolsCall(makeToolCallRequest(moduleEnableToolName))
	})

	if strings.Contains(out, "tools/list_changed") {
		t.Errorf("an unchanged list was announced: %s", out)
	}
}

// The toggle already succeeded on the account. A refetch that then fails
// must not read as "the module was not switched on"; it names the one
// call that recovers.
func TestAFailedRefetchKeepsTheToggleSuccessful(t *testing.T) {
	srv := moduleServer(&fakeModuleAPI{on: false, updateErr: fmt.Errorf("dial tcp: connection refused")})

	result := toolCallResult(t, srv.handleToolsCall(makeToolCallRequest(moduleEnableToolName)))

	if result.IsError {
		t.Errorf("a failed refetch must not fail the toggle: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "virt is on") {
		t.Errorf("the toggle's own answer was lost: %s", text)
	}
	if !strings.Contains(text, manifestUpdateToolName) {
		t.Errorf("nothing told the agent how to recover: %s", text)
	}
}

// A server started without a loader (the shape a bare NewServer has) must
// not panic on a toggle; it simply cannot refresh.
func TestAToggleWithNoLoaderIsNotAnError(t *testing.T) {
	srv := &Server{manifest: moduleManifest(false), executor: &fakeModuleAPI{on: true}, category: "write", bootstrapped: true}

	resp := srv.handleToolsCall(makeToolCallRequest(moduleDisableToolName))
	if resp.Result.(CallToolResult).IsError {
		t.Error("a toggle on a loaderless server must still succeed")
	}
}

func TestIsModuleToggleTool(t *testing.T) {
	for name, want := range map[string]bool{
		moduleEnableToolName:      true,
		moduleDisableToolName:     true,
		"account_modules":         false,
		"services_postgresql_add": false,
		"manifest_update":         false,
		"":                        false,
	} {
		if got := isModuleToggleTool(name); got != want {
			t.Errorf("isModuleToggleTool(%q) = %v, want %v", name, got, want)
		}
	}
}

// The integration test proves the loader, the executor and the server
// together against a real HTTP conductor, in one process and with no
// restart. The seam tests above fake the reloader; this one does not, so
// it is what catches a scoped route the loader never asks for.
func TestModuleToggleEndToEndAgainstAFakeConductor(t *testing.T) {
	const aid = "acct1"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.Unsetenv("RUNOS_API_KEY")
	os.Unsetenv("RUNOS_ACCOUNT_ID")
	configDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := fmt.Sprintf(`{"api_key":"pat-test-token","account_id":%q}`, aid)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var mu sync.Mutex
	enabled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/"+aid+"/modules/virt/enable", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enabled = true
		mu.Unlock()
		fmt.Fprint(w, `{"key":"virt","enabled":true}`)
	})
	mux.HandleFunc("/"+aid+"/modules/virt/disable", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		enabled = false
		mu.Unlock()
		fmt.Fprint(w, `{"key":"virt","enabled":false}`)
	})
	mux.HandleFunc("/"+aid+"/cli/manifest-version", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		on := enabled
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"version": moduleVersion(on)})
	})
	mux.HandleFunc("/"+aid+"/cli/manifest", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		on := enabled
		mu.Unlock()
		body, _ := json.Marshal(moduleManifest(on))
		w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	loader := manifest.NewLoader(srv.URL, configDir)
	m, err := loader.Load()
	if err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	// The executor and the server share ONE manifest pointer, which is how
	// a refresh reaches both without any plumbing between them.
	server := NewServer(m, NewCommandExecutor(m, srv.URL), "test", "write")
	server.SetManifestReloader(loader)
	server.bootstrapped = true

	if before := listedTools(t, server); slices.Contains(before, "vms_list") {
		t.Fatalf("virt is off, so vms_list must not be listed: %v", before)
	}

	out := captureStdout(t, func() {
		result := toolCallResult(t, server.handleToolsCall(toggleRequest(moduleEnableToolName)))
		if result.IsError {
			t.Fatalf("enable failed: %s", result.Content[0].Text)
		}
	})
	if !strings.Contains(out, "tools/list_changed") {
		t.Errorf("the enable did not announce the wider list: %s", out)
	}
	if after := listedTools(t, server); !slices.Contains(after, "vms_list") {
		t.Errorf("vms_list missing after enable, with no restart in between: %v", after)
	}

	out = captureStdout(t, func() {
		result := toolCallResult(t, server.handleToolsCall(toggleRequest(moduleDisableToolName)))
		if result.IsError {
			t.Fatalf("disable failed: %s", result.Content[0].Text)
		}
	})
	if !strings.Contains(out, "tools/list_changed") {
		t.Errorf("the disable did not announce the narrower list: %s", out)
	}
	if after := listedTools(t, server); slices.Contains(after, "vms_list") {
		t.Errorf("vms_list survived the disable: %v", after)
	}
}

// moduleVersion is the shape conductor serves: the manifest version, plus
// the enabled module keys as semver build metadata (FPL31 D11).
func moduleVersion(virtOn bool) string {
	if virtOn {
		return "45.3.0+virt"
	}
	return "45.3.0"
}

// S7 / FPL31 D4. Switching a module off is NOT a destructive verb, so it
// takes no `confirm` under MCP and no --yes on the CLI.
//
// Conductor refuses a disable with 409 module.in_use while the account
// still owns anything the module governs, so a disable can never cut
// access to a live machine, and an enable is never blocked, so recovery
// is one call. Adding "disable" to the destructive verb list would gate
// every future disable verb with it.
func TestAModuleDisableNeedsNoConfirmation(t *testing.T) {
	for _, cmd := range moduleManifest(false).Commands {
		if dynacmd.IsDestructiveCommand(cmd) {
			t.Errorf("%s was classified destructive; a module toggle takes no confirmation", cmd.Command)
		}
	}
}
