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
	fixture, err := os.ReadFile("../internal/services/testdata/manifest-create-affinity-49.4.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var served manifest.Manifest
	if err := json.Unmarshal(fixture, &served); err != nil {
		t.Fatal(err)
	}
	served.Commands = append(served.Commands,
		manifest.Command{Command: "services/valkey/{id}/update", Method: http.MethodPatch, Endpoint: "/:aid/:cid/services/valkey/:id", Input: &manifest.Input{Fields: []manifest.Field{{Name: "id", Type: "string", Positional: true}}}},
		manifest.Command{Command: "services/valkey/{id}/show", Method: http.MethodGet, Endpoint: "/:aid/:cid/services/valkey/:id"},
	)
	manifestBody, err := json.Marshal(served)
	if err != nil {
		t.Fatal(err)
	}
	forms := []struct{ name, affinity string }{
		{"omitted", ""},
		{"bare", "nodeAffinityTags:\n"},
		{"null", "nodeAffinityTags: null\n"},
		{"Null", "nodeAffinityTags: Null\n"},
		{"NULL", "nodeAffinityTags: NULL\n"},
		{"tilde", "nodeAffinityTags: ~\n"},
		{"short tag", "nodeAffinityTags: !!null null\n"},
		{"full tag", "nodeAffinityTags: !<tag:yaml.org,2002:null> null\n"},
	}
	for _, tc := range forms {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			refuseNext := tc.name == "bare"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/acct1/cli/manifest-version":
					fmt.Fprintf(w, `{"version":%q}`, served.Version)
				case "/acct1/cli/manifest":
					w.Write(manifestBody)
				case "/acct1/cluster1/services/valkey":
					calls++
					body, err := io.ReadAll(r.Body)
					if r.Method != http.MethodPost || err != nil || string(body) != "{}" || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("request = %s %s, body = %q, content type = %q, error = %v", r.Method, r.URL.Path, body, r.Header.Get("Content-Type"), err)
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":"a JSON object is required"}`)
						return
					}
					if refuseNext {
						refuseNext = false
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"error":"controlled create refusal"}`)
						return
					}
					w.WriteHeader(http.StatusAccepted)
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
			if err := os.WriteFile(path, []byte("type: valkey\ncid: cluster1\naid: acct1\n"+tc.affinity), 0o600); err != nil {
				t.Fatal(err)
			}
			dryCmd := &cobra.Command{}
			dryCmd.Flags().Bool("dry-run", true, "")
			var dryErr error
			dryOut := captureStdout(t, func() { dryErr = runServicesSync(dryCmd, []string{path}) })
			if dryErr != nil || calls != 0 || !strings.Contains(dryOut, "POST") || strings.Contains(dryOut, "nodeAffinityTags") {
				t.Fatalf("text preview = %q, calls = %d, error = %v", dryOut, calls, dryErr)
			}
			jsonCmd := &cobra.Command{}
			jsonCmd.Flags().Bool("dry-run", true, "")
			jsonCmd.Flags().Bool("json", true, "")
			jsonOut := captureStdout(t, func() { dryErr = runServicesSync(jsonCmd, []string{path}) })
			if dryErr != nil || calls != 0 || !strings.Contains(jsonOut, `"createBody": {}`) || strings.Contains(jsonOut, "nodeAffinityTags") {
				t.Fatalf("JSON preview = %q, calls = %d, error = %v", jsonOut, calls, dryErr)
			}
			beforeApply, err := services.Load(path)
			if err != nil || beforeApply.ID != "" {
				t.Fatalf("dry run changed YAML: %#v, error = %v", beforeApply, err)
			}
			if tc.name == "bare" {
				var refusal error
				captureStdout(t, func() { refusal = runServicesSync(&cobra.Command{}, []string{path}) })
				afterRefusal, err := services.Load(path)
				if refusal == nil || !strings.Contains(refusal.Error(), "controlled create refusal") || calls != 1 || err != nil || afterRefusal.ID != "" {
					t.Fatalf("refusal = %v, calls = %d, YAML = %#v, load error = %v", refusal, calls, afterRefusal, err)
				}
			}
			var runErr error
			out := captureStdout(t, func() { runErr = runServicesSync(&cobra.Command{}, []string{path}) })
			wantCalls := 1
			if tc.name == "bare" {
				wantCalls = 2
			}
			if runErr != nil || calls != wantCalls || !strings.Contains(out, "Provisioned valkey/abc12") || !strings.Contains(out, "job-1") {
				t.Fatalf("sync output = %q, calls = %d, error = %v", out, calls, runErr)
			}
			saved, err := services.Load(path)
			if err != nil || saved.ID != "abc12" {
				t.Fatalf("saved service = %#v, error = %v", saved, err)
			}
		})
	}
}
