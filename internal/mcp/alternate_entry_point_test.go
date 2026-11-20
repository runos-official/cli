package mcp

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Story 347 criterion 3 (finding 1467): one row per alternate entry
// point this story's own plan inventory (FPL439, "Current execution
// inventory") names, proving that a scoped denial from an app-scoped
// route is final on every one of them: exactly one request reaches the
// server (no fallback to a broader or legacy route), the request
// target is exactly the app-scoped endpoint the caller asked for (no
// account-wide or cluster-wide substitution), the resolved bearer is
// the one explicit test token (and never leaks into output or error
// text), and the server's structured refusal fields reach the caller
// unchanged.
//
// The four entry points, each its own test function so a listed one
// with no test fails visibly rather than silently sharing a table row:
//  1. the generated MCP executor, CommandExecutor.Execute;
//  2. the module enable/disable tools, which dispatch through that
//     same executor (internal/mcp/module_toggle.go);
//  3. direct typed dispatch on the dynamic executor, ExecuteWithInput
//     (internal/dynacmd);
//  4. a static MCP subprocess tool (jobs_follow), run as the REAL
//     compiled runos binary -- not mocked -- so the proof covers the
//     actual process boundary a static tool crosses.

const entryPointTestToken = "test-entry-point-token"

type recordedEntryPointRequest struct {
	method        string
	path          string
	authorization string
}

// startDenialRecorder returns a loopback server that records every
// request it receives and answers every one of them with a 403 and
// denialBody, plus the slice those requests land in.
func startDenialRecorder(t *testing.T, denialBody string) (*httptest.Server, *[]recordedEntryPointRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedEntryPointRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedEntryPointRequest{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization")})
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(denialBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// startDenialRecorderWithManifestBootstrap is startDenialRecorder, but
// answers /cli/manifest-version and /cli/manifest (any account prefix)
// successfully with an empty manifest, denying every other request with
// denialBody. Only row 4 (the real CLI binary) needs this: cmd/root.go
// builds the full cobra command tree, dynamic commands included, before
// routing to ANY command, static or not, so even a purely static
// command like `follow` triggers the account-scoped manifest fetch
// first. That bootstrap is unrelated to this row's own claim and must
// succeed, not be swept into "no fallback after the denial" below.
func startDenialRecorderWithManifestBootstrap(t *testing.T, denialBody string) (*httptest.Server, *[]recordedEntryPointRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedEntryPointRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedEntryPointRequest{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization")})
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/cli/manifest-version"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
		case strings.HasSuffix(r.URL.Path, "/cli/manifest"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"1.0.0","commands":[]}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(denialBody))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// setupEntryPointConfig points config.Load() at a throwaway HOME
// carrying a config naming apiURL and entryPointTestToken, and clears
// every environment variable the config getters would otherwise
// prefer, so no test depends on a developer's real config or shell.
// Returns the HOME directory, for a caller (row 4) that must build its
// OWN subprocess environment rather than inherit this process's.
func setupEntryPointConfig(t *testing.T, apiURL string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := fmt.Sprintf(
		`{"account_id":"acct1","default_cluster_id":"cluster1","api_key":%q,"conductor_url":%q}`,
		entryPointTestToken, apiURL,
	)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HOME", home)
	// Finding 1467 fix-up: an environment variable that is PRESENT but
	// EMPTY is a deliberate, different signal from UNSET (the CLI
	// refuses explicit-empty RUNOS_API_KEY rather than silently falling
	// back, "Preserve the explicit-empty environment refusal"). Empty
	// is fine for rows 1-3, which resolve the token through
	// auth.ResolveToken directly and never hit that cmd-level guard;
	// row 4 (the real binary) builds its own unset-not-empty env, see
	// buildEntryPointSubprocessEnv.
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_CLUSTER_ID", "")
	t.Setenv("RUNOS_API_URL", "")
	return home
}

// buildEntryPointSubprocessEnv returns an environment for row 4's real
// subprocess that OMITS (never merely empties) every RUNOS_* variable
// the config getters would otherwise prefer over the stored config
// file, plus HOME pointing at home. See setupEntryPointConfig's note:
// an empty value is refused outright by the real CLI, unlike an absent
// one, which falls back to the stored api_key as intended.
func buildEntryPointSubprocessEnv(home string) []string {
	drop := map[string]bool{
		"RUNOS_API_KEY": true, "RUNOS_ACCOUNT_ID": true,
		"RUNOS_CLUSTER_ID": true, "RUNOS_API_URL": true, "HOME": true,
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && drop[name] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home)
}

// assertDenialFinal is the shared assertion for every row: a scoped
// denial surfaces as an error, exactly one request was ever sent, it
// targeted wantPath with the one expected bearer, the bearer appears
// nowhere in the error text, and mustContain (a fragment of the
// server's structured refusal) reaches the caller unchanged.
func assertDenialFinal(t *testing.T, reqs []recordedEntryPointRequest, wantPath string, err error, errText, mustContain string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the scoped denial to surface as an error, got nil")
	}
	if len(reqs) != 1 {
		t.Fatalf("recorded %d requests, want exactly 1 (no fallback to a broader or legacy route): %+v", len(reqs), reqs)
	}
	if reqs[0].path != wantPath {
		t.Errorf("request path = %q, want %q (no account-wide/cluster-wide substitution)", reqs[0].path, wantPath)
	}
	wantAuth := "Bearer " + entryPointTestToken
	if reqs[0].authorization != wantAuth {
		t.Errorf("Authorization = %q, want %q (same resolved bearer every entry point must use)", reqs[0].authorization, wantAuth)
	}
	if strings.Contains(errText, entryPointTestToken) {
		t.Errorf("the bearer leaked into caller-visible text: %q", errText)
	}
	if !strings.Contains(errText, mustContain) {
		t.Errorf("the structured refusal (%q) did not reach the caller unchanged: %q", mustContain, errText)
	}
}

// 1. The generated MCP executor: CommandExecutor.Execute.
func TestAlternateEntryPoint_GeneratedMCPExecutor(t *testing.T) {
	const denialBody = `{"error":"forbidden","code":"app.access_denied","reason":"this credential's grant does not cover this app"}`
	srv, reqs := startDenialRecorder(t, denialBody)
	setupEntryPointConfig(t, srv.URL)

	cmdDef := manifest.Command{
		Command: "apps/show", Method: http.MethodGet,
		Endpoint: "/:aid/:cid/apps/:id",
		MCP:      []string{"read"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true},
		}},
	}
	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	_, err := NewCommandExecutor(m, srv.URL).Execute("apps_show", map[string]any{"id": "app-A", "cid": "cluster1"})

	errText := ""
	if err != nil {
		errText = err.Error()
	}
	assertDenialFinal(t, *reqs, "/acct1/cluster1/apps/app-A", err, errText, "app.access_denied")
}

// 2. The module enable/disable tools: they dispatch through the SAME
// generated executor and then would refresh tools/list, but only on
// success (handleModuleToggle returns the error immediately, before
// any refresh request).
func TestAlternateEntryPoint_ModuleToggleTool(t *testing.T) {
	const denialBody = `{"error":"forbidden","code":"module.access_denied","reason":"this credential cannot change account modules"}`
	srv, reqs := startDenialRecorder(t, denialBody)
	setupEntryPointConfig(t, srv.URL)

	cmdDef := manifest.Command{
		Command: "account/modules/enable", Method: http.MethodPost,
		Endpoint: "/:aid/account/modules/:key/enable",
		MCP:      []string{"write"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "key", Type: "string", Positional: true},
		}},
	}
	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	s := NewServer(m, NewCommandExecutor(m, srv.URL), "1.0-test", "write")
	_, err := s.handleModuleToggle(moduleEnableToolName, map[string]any{"key": "vm"})

	errText := ""
	if err != nil {
		errText = err.Error()
	}
	assertDenialFinal(t, *reqs, "/acct1/account/modules/vm/enable", err, errText, "module.access_denied")
}

// 3. Direct typed dispatch on the dynamic executor: ExecuteWithInput,
// against an app-scoped logs endpoint -- the exact "obtain broad logs"
// shape criterion 3 names, through the entry point that bypasses both
// cobra flag parsing and the MCP tool schema.
func TestAlternateEntryPoint_DirectTypedDispatch(t *testing.T) {
	const denialBody = `{"error":"forbidden","code":"apps.logs_denied","reason":"this credential's grant does not cover this app's logs"}`
	srv, reqs := startDenialRecorder(t, denialBody)
	setupEntryPointConfig(t, srv.URL)

	cmdDef := manifest.Command{
		Command: "apps/logs", Method: http.MethodGet,
		Endpoint: "/:aid/:cid/apps/:id/logs",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true},
		}},
	}
	_, err := dynacmd.NewExecutor(srv.URL).ExecuteWithInput(cmdDef, []string{"app-A"}, map[string]any{}, "cluster1")

	errText := ""
	if err != nil {
		errText = err.Error()
	}
	assertDenialFinal(t, *reqs, "/acct1/cluster1/apps/app-A/logs", err, errText, "apps.logs_denied")
}

// 4. A static MCP subprocess tool (jobs_follow), run as the REAL
// compiled runos binary: handleJobsCommand/handleAppsCommand/etc. find
// their subprocess target with os.Executable(), which always resolves
// to the actual running process and cannot be swapped for a test
// double in-process. Building and exec'ing the real binary is the only
// way to prove this entry point without mocking away the one thing
// that makes it a DIFFERENT entry point: the process boundary.
//
// jobs_follow's default mode (no --scoped) calls
// jobs.FollowJobWithServiceToWriter, whose loop calls GetStatus once
// and returns immediately on any non-2xx (internal/jobs/follow.go):
// GetWorkItems, the second call a successful follow would make, is
// never reached, so "exactly one request" holds for the denial case
// this test drives, independent of how many calls a full follow makes.
func TestAlternateEntryPoint_StaticSubprocessTool(t *testing.T) {
	binPath := buildRunosBinaryForTest(t)

	const denialBody = `{"error":"forbidden","code":"jobs.access_denied","reason":"this credential's grant does not cover this job"}`
	srv, reqs := startDenialRecorderWithManifestBootstrap(t, denialBody)
	home := setupEntryPointConfig(t, srv.URL)

	const jobID = "11111111-2222-3333-4444-555555555555"
	cmd := exec.Command(binPath, "follow", jobID)
	// NOT the inherited environment: this process's RUNOS_* vars are
	// explicit-but-empty (setupEntryPointConfig, for rows 1-3's
	// in-process auth.ResolveToken calls), which the real CLI's own
	// startup refuses outright as a likely mistake, distinct from
	// those vars being absent. The real subprocess a static MCP tool
	// launches inherits the MCP SERVER's environment, which never sets
	// RUNOS_API_KEY to "" either, so an absent var is the faithful
	// shape here.
	cmd.Env = buildEntryPointSubprocessEnv(home)
	out, runErr := cmd.CombinedOutput()

	if runErr == nil {
		t.Fatalf("expected the subprocess to fail on the scoped denial, got success: %s", out)
	}
	// The real CLI's cobra tree build fetches the account-scoped dynamic
	// manifest before routing to ANY command, static or not (two calls,
	// /cli/manifest-version and /cli/manifest, now answered successfully
	// by startDenialRecorderWithManifestBootstrap). That bootstrap is
	// unrelated to this row's claim: it runs identically whether the
	// job follow below is permitted or denied, so it is filtered out
	// before applying the shared "no fallback after the denial" check
	// to the one business endpoint this row actually targets.
	business := make([]recordedEntryPointRequest, 0, len(*reqs))
	for _, r := range *reqs {
		if strings.HasPrefix(r.path, "/jobs/") {
			business = append(business, r)
		}
	}
	assertDenialFinal(t, business, "/jobs/"+jobID, runErr, string(out), "jobs.access_denied")
}

// buildRunosBinaryForTest compiles the actual runos binary (this
// repository's main package) once per test, so
// TestAlternateEntryPoint_StaticSubprocessTool exercises the real CLI
// a static MCP tool would re-exec, not a stand-in. Reuses the normal Go
// build cache, so this is fast after the package has been built once in
// the same environment (as every other gate in this story already has).
func buildRunosBinaryForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "runos")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = "../.." // internal/mcp -> repo root
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to build the runos binary for the subprocess test: %v\n%s", err, out)
	}
	return binPath
}
