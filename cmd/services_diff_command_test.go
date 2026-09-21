package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/services"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// TestServiceDiffProcess runs one command in a child process. Drift exits the
// process, so the parent test must inspect its status outside this process.
func TestServiceDiffProcess(t *testing.T) {
	mode := os.Getenv("RUNOS_TEST_SERVICE_COMMAND")
	if mode == "" {
		return
	}
	command := &cobra.Command{Use: mode, SilenceUsage: true}
	command.Flags().String("cid", "", "cluster")
	command.Flags().Bool("json", false, "JSON")
	if mode == "diff" {
		command.RunE = runServicesDiff
	} else {
		command.RunE = runServicesPull
		command.Flags().Bool("force", false, "force")
		command.Flags().String("out", "", "out")
		command.Flags().String("type", "", "type")
		command.Flags().String("id", "", "id")
	}
	args := []string{os.Getenv("RUNOS_TEST_SERVICE_FILE")}
	if os.Getenv("RUNOS_TEST_SERVICE_JSON") == "1" {
		args = append(args, "--json")
	}
	if os.Getenv("RUNOS_TEST_SERVICE_FORCE") == "1" {
		args = append(args, "--force")
	}
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type serviceCommandFixture struct {
	server    *httptest.Server
	methods   atomic.Int32
	mutations atomic.Int32
}

func newServiceCommandFixture(t *testing.T) *serviceCommandFixture {
	t.Helper()
	f := &serviceCommandFixture{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.methods.Add(1)
		if r.Method != http.MethodGet {
			f.mutations.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/cli/manifest-version"):
			fmt.Fprint(w, `{"version":"test"}`)
		case strings.HasSuffix(r.URL.Path, "/cli/manifest"):
			fmt.Fprint(w, `{"version":"test","commands":[]}`)
		case strings.HasSuffix(r.URL.Path, "/services/postgresql/abc12"):
			fmt.Fprint(w, `{"id":"abc12","name":"old-name","replicas":1,"nodeAffinityTags":["gpu:shared"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func serviceCommandManifest(t *testing.T) []byte {
	t.Helper()
	fields := []manifest.Field{{Name: "id", Type: "string", Positional: true}, {Name: "name", Type: "string"}, {Name: "replicas", Type: "integer"}, {Name: "nodeAffinityTags", Type: "array", Clearable: true}}
	m := manifest.Manifest{Version: "test", Commands: []manifest.Command{
		{Command: "services/postgresql/{id}/show", Method: "GET", Endpoint: "/:aid/:cid/services/postgresql/:id", Input: &manifest.Input{Fields: []manifest.Field{{Name: "id", Type: "string"}}}},
		{Command: "services/postgresql/add", Method: "POST", Endpoint: "/:aid/:cid/services/postgresql", Input: &manifest.Input{Fields: fields}},
		{Command: "services/postgresql/{id}/update", Method: "PATCH", Endpoint: "/:aid/:cid/services/postgresql/:id", Input: &manifest.Input{Fields: fields}},
	}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runServiceCommand(t *testing.T, fixture *serviceCommandFixture, mode, content string, jsonOut, force bool) (string, string, int, string) {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"config.json":      []byte(fmt.Sprintf(`{"api_key":"test-pat","account_id":"acct1","conductor_url":%q}`, fixture.server.URL)),
		"manifest.json":    serviceCommandManifest(t),
		"manifest.account": []byte("acct1"),
	} {
		if err := os.WriteFile(filepath.Join(configDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "service.yaml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestServiceDiffProcess$")
	child.Env = append(os.Environ(), "HOME="+home, "RUNOS_TEST_SERVICE_COMMAND="+mode,
		"RUNOS_TEST_SERVICE_FILE="+path, "RUNOS_API_URL="+fixture.server.URL,
		"RUNOS_API_KEY=test-pat", "RUNOS_ACCOUNT_ID=acct1")
	if jsonOut {
		child.Env = append(child.Env, "RUNOS_TEST_SERVICE_JSON=1")
	}
	if force {
		child.Env = append(child.Env, "RUNOS_TEST_SERVICE_FORCE=1")
	}
	var stdout, stderr strings.Builder
	child.Stdout, child.Stderr = &stdout, &stderr
	err := child.Run()
	exitCode := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return stdout.String(), stderr.String(), exitCode, path
}

func TestServicesDiffCommandReportsSemanticState(t *testing.T) {
	fixture := newServiceCommandFixture(t)
	header := "type: postgresql\nid: abc12\ncid: cluster1\naid: acct1\n"
	for _, tc := range []struct {
		name, fields string
		status       string
		exitCode     int
	}{
		{"bare", "name: old-name\nreplicas: 1\nnodeAffinityTags:\n", "in_sync", 0},
		{"comment", "# local note\nname: old-name\nreplicas: 1\nnodeAffinityTags: [gpu:shared]\n", "in_sync", 0},
		{"replicas", "name: old-name\nreplicas: 2\nnodeAffinityTags:\n", "drift", 2},
		{"removal", "name: old-name\nreplicas: 1\n", "drift", 2},
	} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, jsonOut), func(t *testing.T) {
				before := fixture.mutations.Load()
				stdout, stderr, exitCode, _ := runServiceCommand(t, fixture, "diff", header+tc.fields, jsonOut, false)
				if exitCode != tc.exitCode || fixture.mutations.Load() != before {
					t.Fatalf("exit=%d mutations=%d stdout=%q stderr=%q", exitCode, fixture.mutations.Load()-before, stdout, stderr)
				}
				wantStatus := tc.status
				if !jsonOut && wantStatus == "in_sync" {
					wantStatus = "in sync"
				}
				if !strings.Contains(stdout, wantStatus) {
					t.Fatalf("status %q absent: stdout=%q stderr=%q", tc.status, stdout, stderr)
				}
				if jsonOut {
					var result struct {
						Status string `json:"status"`
					}
					if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Status != tc.status {
						t.Fatalf("invalid JSON diff: %q (%v)", stdout, err)
					}
				}
				if tc.name == "replicas" && strings.Contains(stdout, "nodeAffinityTags") {
					t.Fatalf("preview includes unchanged pin: %q", stdout)
				}
				if tc.name == "removal" && !strings.Contains(stdout, "nodeAffinityTags") {
					t.Fatalf("preview hides pin removal: %q", stdout)
				}
			})
		}
	}
	if fixture.methods.Load() == 0 {
		t.Fatal("show responder was not exercised")
	}
}

func TestServicesPullProtectsSemanticallyEqualLocalText(t *testing.T) {
	fixture := newServiceCommandFixture(t)
	header := "type: postgresql\nid: abc12\ncid: cluster1\naid: acct1\n"
	for _, tc := range []struct{ name, fields string }{
		{"comment", "# local note\nname: old-name\nreplicas: 1\nnodeAffinityTags: [gpu:shared]\n"},
		{"bare", "name: old-name\nreplicas: 1\nnodeAffinityTags:\n"},
	} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, jsonOut), func(t *testing.T) {
				content := header + tc.fields
				stdout, stderr, exitCode, path := runServiceCommand(t, fixture, "pull", content, jsonOut, false)
				if exitCode != 1 || !strings.Contains(stdout+stderr, "local drift") {
					t.Fatalf("pull refusal: exit=%d stdout=%q stderr=%q", exitCode, stdout, stderr)
				}
				if jsonOut {
					var summary struct {
						Drifted bool `json:"drifted"`
					}
					if err := json.Unmarshal([]byte(stdout), &summary); err != nil || !summary.Drifted {
						t.Fatalf("JSON refusal lacks drift: %q (%v)", stdout, err)
					}
				} else if !strings.Contains(stdout, "--- local") {
					t.Fatalf("text refusal lacks raw diff: %q", stdout)
				}
				original, err := os.ReadFile(path)
				if err != nil || string(original) != content {
					t.Fatalf("unforced pull changed bytes: %q (%v)", original, err)
				}
				if fixture.mutations.Load() != 0 {
					t.Fatalf("pull sent %d mutation requests", fixture.mutations.Load())
				}
				_, stderr, exitCode, path = runServiceCommand(t, fixture, "pull", content, jsonOut, true)
				if exitCode != 0 {
					t.Fatalf("forced pull: exit=%d stderr=%q", exitCode, stderr)
				}
				written, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				expected, err := yaml.Marshal(&services.ServiceYAML{Type: "postgresql", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{
					"name": "old-name", "replicas": float64(1), "nodeAffinityTags": []any{"gpu:shared"},
				}})
				if err != nil || string(written) != string(expected) {
					t.Fatalf("forced pull wrote %q, want %q (%v)", written, expected, err)
				}
			})
		}
	}
}

func TestServicesDiffCommandRetainsFileAndIdentityErrors(t *testing.T) {
	fixture := newServiceCommandFixture(t)
	for _, tc := range []struct{ name, content, want string }{
		{"missing", "", "not found"},
		{"malformed", "type: [\n", "read yaml"},
		{"account mismatch", "type: postgresql\nid: abc12\ncid: cluster1\naid: acct2\n", "logged in as"},
	} {
		for _, jsonOut := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, jsonOut), func(t *testing.T) {
				before := fixture.methods.Load()
				stdout, stderr, exitCode, _ := runServiceCommand(t, fixture, "diff", tc.content, jsonOut, false)
				if exitCode != 1 || !strings.Contains(stdout+stderr, tc.want) {
					t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout, stderr)
				}
				if fixture.methods.Load()-before > 1 || fixture.mutations.Load() != 0 {
					t.Fatalf("file error reached service API: requests=%d mutations=%d", fixture.methods.Load()-before, fixture.mutations.Load())
				}
			})
		}
	}
}

func TestServicesDiffCommandRetainsUnprovisionedResponse(t *testing.T) {
	fixture := newServiceCommandFixture(t)
	content := "type: postgresql\ncid: cluster1\naid: acct1\nname: new-name\n"
	for _, jsonOut := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOut), func(t *testing.T) {
			before := fixture.methods.Load()
			stdout, stderr, exitCode, _ := runServiceCommand(t, fixture, "diff", content, jsonOut, false)
			want := "has not been provisioned"
			if jsonOut {
				want = "local_missing"
			}
			if exitCode != 0 || !strings.Contains(stdout, want) || fixture.methods.Load()-before > 1 {
				t.Fatalf("unprovisioned result: exit=%d stdout=%q stderr=%q", exitCode, stdout, stderr)
			}
		})
	}
}
