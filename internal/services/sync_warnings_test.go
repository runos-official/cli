package services

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
)

// Objective 92 / story 211, review cycle 1. `runos services sync` applies
// its plan through dynacmd.ExecuteWithInput, NOT through the executor's
// Execute, so it never reached the generic advisory renderer installed
// there. Both branches of ApplySyncPlan then unmarshal three id fields
// and discard the rest of the body, so conductor's `warnings` array was
// dropped outright on the declarative path while the imperative
// `runos services vllm update` printed it.
//
// This is the path an operator takes to change a served model name or a
// replica count from a checked-in yaml, which is exactly the change the
// objective wants a warning attached to.
//
// Every id, name and warning string below is a placeholder.

const (
	syncTestAccountID = "acct1"
	syncTestClusterID = "cluster1"
	syncTestPAT       = "pat-test-token"
)

// syncStub answers any request with the given body.
func syncStub(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// syncEnv points config.Load at a throwaway home carrying a PAT config
// and clears the environment variables the config getters prefer, so the
// test touches nothing but the caller's own httptest server.
func syncEnv(t *testing.T, apiURL string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := fmt.Sprintf(
		`{"account_id":%q,"default_cluster_id":%q,"api_key":%q,"conductor_url":%q}`,
		syncTestAccountID, syncTestClusterID, syncTestPAT, apiURL,
	)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_CLUSTER_ID", "")
	t.Setenv("RUNOS_API_URL", "")
}

func TestApplySyncPlan_CarriesConductorAdvisoryWarnings(t *testing.T) {
	m := fakeManifest(t)
	addCmd, err := AddCommand(m, "postgresql")
	if err != nil {
		t.Fatalf("add command: %v", err)
	}
	updateCmd, err := UpdateCommand(m, "postgresql")
	if err != nil {
		t.Fatalf("update command: %v", err)
	}

	t.Run("update path", func(t *testing.T) {
		srv := syncStub(t, `{"jobId":"job-1","warnings":["first advisory","second advisory"]}`)
		syncEnv(t, srv.URL)
		exec := dynacmd.NewExecutor(srv.URL)

		plan := &SyncPlan{
			Type:      "postgresql",
			ID:        "abc12",
			CID:       syncTestClusterID,
			PatchBody: map[string]any{"replicas": 2},
		}
		res, err := ApplySyncPlan(exec, plan, addCmd, updateCmd)
		if err != nil {
			t.Fatalf("ApplySyncPlan: %v", err)
		}
		if res.JobID != "job-1" {
			t.Errorf("JobID = %q, want job-1", res.JobID)
		}
		want := []string{"first advisory", "second advisory"}
		if len(res.Warnings) != len(want) {
			t.Fatalf("Warnings = %#v, want %#v", res.Warnings, want)
		}
		for i := range want {
			if res.Warnings[i] != want[i] {
				t.Errorf("Warnings[%d] = %q, want %q", i, res.Warnings[i], want[i])
			}
		}
	})

	t.Run("create path", func(t *testing.T) {
		srv := syncStub(t, `{"jobId":"job-2","osid":"postgresql-abc12","warnings":["create advisory"]}`)
		syncEnv(t, srv.URL)
		exec := dynacmd.NewExecutor(srv.URL)

		plan := &SyncPlan{
			Type:       "postgresql",
			CID:        syncTestClusterID,
			CreateBody: map[string]any{"name": "lane-one"},
		}
		res, err := ApplySyncPlan(exec, plan, addCmd, updateCmd)
		if err != nil {
			t.Fatalf("ApplySyncPlan: %v", err)
		}
		if res.NewID != "abc12" {
			t.Errorf("NewID = %q, want abc12", res.NewID)
		}
		if len(res.Warnings) != 1 || res.Warnings[0] != "create advisory" {
			t.Errorf("Warnings = %#v, want the single create advisory", res.Warnings)
		}
	})

	t.Run("no warnings key leaves the slice empty", func(t *testing.T) {
		srv := syncStub(t, `{"jobId":"job-3"}`)
		syncEnv(t, srv.URL)
		exec := dynacmd.NewExecutor(srv.URL)

		plan := &SyncPlan{
			Type:      "postgresql",
			ID:        "abc12",
			CID:       syncTestClusterID,
			PatchBody: map[string]any{"replicas": 2},
		}
		res, err := ApplySyncPlan(exec, plan, addCmd, updateCmd)
		if err != nil {
			t.Fatalf("ApplySyncPlan: %v", err)
		}
		if len(res.Warnings) != 0 {
			t.Errorf("Warnings = %#v, want none", res.Warnings)
		}
	})
}

// The renderer the sync command uses is the executor's own, so the line
// an operator sees from `runos services sync` is byte-identical to the
// one they see from `runos services postgresql update`. Pinned here so a
// future change cannot grow a second wording on this path.
func TestApplySyncPlanWarningsRenderWithTheSharedRenderer(t *testing.T) {
	var sb strings.Builder
	dynacmd.PrintAdvisoryWarnings(&sb, []string{"first advisory", "second advisory"})
	want := "Warning: first advisory\nWarning: second advisory\n"
	if sb.String() != want {
		t.Errorf("output = %q, want %q", sb.String(), want)
	}
}
