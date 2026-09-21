package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/services"
	"github.com/spf13/cobra"
)

func TestServicesSyncEmptyCreateWritesReturnedID(t *testing.T) {
	manifestBody, err := json.Marshal(manifest.Manifest{Version: "test-version", Commands: []manifest.Command{
		{Command: "services/valkey/add", Method: http.MethodPost, Endpoint: "/:aid/:cid/services/valkey", Input: &manifest.Input{Fields: []manifest.Field{{Name: "nodeAffinityTags", Type: "array"}}}},
		{Command: "services/valkey/{id}/update", Method: http.MethodPatch, Endpoint: "/:aid/:cid/services/valkey/:id", Input: &manifest.Input{Fields: []manifest.Field{{Name: "id", Type: "string", Positional: true}}}},
		{Command: "services/valkey/{id}/show", Method: http.MethodGet, Endpoint: "/:aid/:cid/services/valkey/:id"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/acct1/cli/manifest-version":
			fmt.Fprint(w, `{"version":"test-version"}`)
		case "/acct1/cli/manifest":
			w.Write(manifestBody)
		case "/acct1/cluster1/services/valkey":
			calls++
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil || len(body) != 0 {
				t.Errorf("empty create body = %q, error = %v", body, err)
			}
			fmt.Fprint(w, `{"osid":"valkey-abc12","jobId":"job-1"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_URL", srv.URL)
	t.Setenv("RUNOS_API_KEY", "pat-test-token")
	t.Setenv("RUNOS_ACCOUNT_ID", "acct1")
	configDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(fmt.Sprintf(`{"account_id":"acct1","api_key":"pat-test-token","conductor_url":%q}`, srv.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "service.yaml")
	if err := os.WriteFile(path, []byte("type: valkey\ncid: cluster1\naid: acct1\nnodeAffinityTags:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() { runErr = runServicesSync(&cobra.Command{}, []string{path}) })
	if runErr != nil || calls != 1 || !strings.Contains(out, "Provisioned valkey/abc12") {
		t.Fatalf("sync output = %q, calls = %d, error = %v", out, calls, runErr)
	}
	saved, err := services.Load(path)
	if err != nil || saved.ID != "abc12" {
		t.Fatalf("saved service = %#v, error = %v", saved, err)
	}
}
