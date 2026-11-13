package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// An MCP tool call refused by the version gate returns the same JSON error shape as any other
// refusal: the code, the status and one plain sentence, never the raw body or the request URL.
func TestAGatedToolCallReturnsTheJSONErrorEnvelope(t *testing.T) {
	var seenHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.UserAgent()
		w.WriteHeader(http.StatusUpgradeRequired)
		fmt.Fprint(w, `{"error":"raw body text","code":"cli.update_required","minimumVersion":"9.9.9"}`)
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	cfg := &config.Config{ConductorURL: srv.URL, AccountID: "aaaaa", APIKey: "pat"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Commands: []manifest.Command{{
		Command: "clusters/list", Endpoint: "/:aid/clusters", Method: http.MethodGet, MCP: []string{"read"},
	}}}

	_, err := NewCommandExecutor(m, srv.URL).Execute("clusters_list", map[string]any{})

	if err == nil {
		t.Fatal("no error")
	}
	if !strings.HasPrefix(seenHeader, "runos-cli/") {
		t.Fatal("the MCP request sent no runos-cli User-Agent")
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(err.Error()), &env); jerr != nil {
		t.Fatalf("error %q is not the JSON envelope: %v", err.Error(), jerr)
	}
	if env["code"] != api.UpdateRequiredCode || env["statusCode"] != float64(426) || !strings.Contains(env["error"].(string), "runos update") {
		t.Fatalf("envelope %v", env)
	}
	if strings.Contains(err.Error(), "raw body text") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("error leaks the body or URL: %q", err.Error())
	}
}
