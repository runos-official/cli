package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/dynacmd"
)

// fakeConductorForDeploy answers the endpoints BuildDiffReport / syncAppState
// hit. The `env` arg seeds BOTH /secret-env-vars (Secret) and /env-vars
// (ConfigMap) responses for callers that don't care about the split; tests
// that need to differentiate can use fakeConductorForDeploySplit below.
func fakeConductorForDeploy(t *testing.T, raw map[string]any, env map[string]string, secrets []apps.SecretFileSummary, overrides []apps.OverrideSummary) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch {
		case strings.HasSuffix(r.URL.Path, "/secret-env-vars"):
			if env == nil {
				env = map[string]string{}
			}
			body = env
		case strings.HasSuffix(r.URL.Path, "/env-vars"):
			// Plain env vars (ConfigMap). Empty by default — tests that
			// need both sides populated should use the split helper.
			body = map[string]string{}
		case strings.HasSuffix(r.URL.Path, "/secret-files"):
			if secrets == nil {
				secrets = []apps.SecretFileSummary{}
			}
			body = map[string]any{"files": secrets}
		case strings.HasSuffix(r.URL.Path, "/overrides"):
			if overrides == nil {
				overrides = []apps.OverrideSummary{}
			}
			body = overrides
		case strings.HasSuffix(r.URL.Path, "/requires"):
			body = map[string]apps.ServiceRequirement{}
		case strings.HasSuffix(r.URL.Path, "/dependencies"):
			// deploy.GetAppDependencies (used by syncAppState) is unchanged.
			body = []deploy.AppDependency{}
		default:
			body = raw
		}
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// syncAppState — localMissingServerHas tracking
// ---------------------------------------------------------------------------

// TestAutoDetectDeployYAML pins the I15-A fix: a multi-yaml directory
// now surfaces the candidate list mirroring `apps diff`'s auto-detect
// instead of the legacy "runos.yaml not found at <path>" dead-end.
// Single-candidate dirs auto-pick (valid OR partial), and empty dirs
// return `("", nil)` so the caller falls back to LoadConfig's own
// not-found message.
func TestAutoDetectDeployYAML(t *testing.T) {
	writeYAML := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	pulledYAML := func(app, id, cid, aid string) string {
		return fmt.Sprintf("app: %q\nid: %q\ncid: %q\naid: %q\n", app, id, cid, aid)
	}
	freshYAML := func(app string) string {
		return fmt.Sprintf("app: %q\n", app)
	}

	t.Run("empty dir returns empty + nil error (caller falls back)", func(t *testing.T) {
		dir := t.TempDir()
		got, err := autoDetectDeployYAML(dir)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("single valid yaml is auto-picked", func(t *testing.T) {
		dir := t.TempDir()
		writeYAML(t, dir, "runos.c1.a1.yaml", pulledYAML("app1", "a1", "c1", "acc1"))
		got, err := autoDetectDeployYAML(dir)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if filepath.Base(got) != "runos.c1.a1.yaml" {
			t.Errorf("got %q, want runos.c1.a1.yaml", got)
		}
	})

	t.Run("single partial (fresh) yaml is auto-picked too", func(t *testing.T) {
		dir := t.TempDir()
		writeYAML(t, dir, "runos.yaml", freshYAML("myapp"))
		got, err := autoDetectDeployYAML(dir)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if filepath.Base(got) != "runos.yaml" {
			t.Errorf("got %q, want runos.yaml", got)
		}
	})

	t.Run("two pulled yamls surface candidate list", func(t *testing.T) {
		dir := t.TempDir()
		writeYAML(t, dir, "runos.c1.a1.yaml", pulledYAML("app1", "a1", "c1", "acc1"))
		writeYAML(t, dir, "runos.c2.a2.yaml", pulledYAML("app2", "a2", "c2", "acc1"))
		_, err := autoDetectDeployYAML(dir)
		if err == nil {
			t.Fatal("expected multi-candidate error, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "multiple runos yaml candidates") {
			t.Errorf("error %q missing multi-candidate phrase", msg)
		}
		if !strings.Contains(msg, "runos.c1.a1.yaml") || !strings.Contains(msg, "runos.c2.a2.yaml") {
			t.Errorf("error %q missing candidate filenames", msg)
		}
		if !strings.Contains(msg, "-c <path>") {
			t.Errorf("error %q missing actionable `-c <path>` hint", msg)
		}
	})

	t.Run("valid + partial mix also surfaces candidate list", func(t *testing.T) {
		dir := t.TempDir()
		writeYAML(t, dir, "runos.c1.a1.yaml", pulledYAML("app1", "a1", "c1", "acc1"))
		writeYAML(t, dir, "runos.yaml", freshYAML("fresh"))
		_, err := autoDetectDeployYAML(dir)
		if err == nil {
			t.Fatal("expected multi-candidate error, got nil")
		}
		if !strings.Contains(err.Error(), "multiple runos yaml candidates") {
			t.Errorf("error %q missing multi-candidate phrase", err.Error())
		}
	})
}

func TestSyncAppState_LocalMissingServerHas_FlagsKeysServerHasButLocalDoesNot(t *testing.T) {
	// Reproduces the user-deletes-key-from-.env, deploy-silently-re-adds
	// scenario. The server has DATABASE_URL + APP_NAME; the local .env
	// has only APP_NAME (user deleted DATABASE_URL). syncAppState must
	// surface DATABASE_URL on the result so runDeploy can warn that
	// the deletion didn't propagate.
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".smoke.env")
	if err := os.WriteFile(envPath, []byte("APP_NAME=smoke\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte("app: smoke\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	srv := fakeConductorForDeploy(t, nil, map[string]string{
		"APP_NAME":     "smoke",
		"DATABASE_URL": "postgresql://server/has/this",
		"FEATURE_FLAG": "true",
	}, nil, nil)
	svc := deploy.NewService(srv.URL, "tok", "mycluster3", "myacct")

	cfg := &deploy.DeployConfig{
		ID:        "nrdhg",
		App:       "smoke",
		SecretEnv: ".smoke.env",
	}
	res, err := syncAppState(svc, cfg, yamlPath, "mycluster3")
	if err != nil {
		t.Fatalf("syncAppState: %v", err)
	}
	want := []string{"DATABASE_URL", "FEATURE_FLAG"}
	if got := res.secretLocalMissingServerHas; !equalStrings(got, want) {
		t.Errorf("secretLocalMissingServerHas = %v, want %v", got, want)
	}
}

func TestSyncAppState_LocalMissingServerHas_EmptyWhenLocalEnvFileAbsent(t *testing.T) {
	// First-time materialisation (no .env on disk yet): every server key
	// IS technically "local-missing", but it's a fresh pull, not a
	// reverted deletion. The warning would be misleading; the field
	// must stay empty.
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte("app: smoke\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	srv := fakeConductorForDeploy(t, nil, map[string]string{
		"APP_NAME":     "smoke",
		"DATABASE_URL": "postgresql://server/has/this",
	}, nil, nil)
	svc := deploy.NewService(srv.URL, "tok", "mycluster3", "myacct")

	cfg := &deploy.DeployConfig{ID: "nrdhg", App: "smoke", SecretEnv: ".smoke.env"}
	res, err := syncAppState(svc, cfg, yamlPath, "mycluster3")
	if err != nil {
		t.Fatalf("syncAppState: %v", err)
	}
	if len(res.secretLocalMissingServerHas) != 0 {
		t.Errorf("expected empty secretLocalMissingServerHas on fresh pull, got %v", res.secretLocalMissingServerHas)
	}
}

// equalStrings compares two string slices for equality (order matters).
// Avoids pulling reflect just for this.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// I3-D regression: syncAppState must swallow 404s on the three GETs
// against /apps/:id/{dependencies,secret-env-vars,env-vars} silently.
// On first deploy the AppDocument hasn't been minted yet (the upcoming
// PrepareDeployment will mint it), so 404 there is the happy path; the
// pre-fix surface emitted three "Warning: failed to fetch ..." lines
// that the user reads as a deploy failure. We assert the function
// returns no error and the result is the zero-value triple, so all
// three branches landed in the suppress path.
func TestSyncAppState_Suppresses404WarningsOnFirstDeploy(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte("app: smoke\n"), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// Server returns 404 for every /apps/:id/... endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"App 'nrdhg' not found"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "tok", "mycluster3", "myacct")
	cfg := &deploy.DeployConfig{ID: "nrdhg", App: "smoke", SecretEnv: ".smoke.env"}
	res, err := syncAppState(svc, cfg, yamlPath, "mycluster3")
	if err != nil {
		t.Fatalf("syncAppState should not propagate 404: %v", err)
	}
	if res == nil {
		t.Fatal("res unexpectedly nil")
	}
	if res.deps != nil {
		t.Errorf("deps should be nil on 404, got %v", res.deps)
	}
	if res.secretEnvVars != nil {
		t.Errorf("secretEnvVars should be nil on 404, got %v", res.secretEnvVars)
	}
	if res.envVars != nil {
		t.Errorf("envVars should be nil on 404, got %v", res.envVars)
	}
}

// ---------------------------------------------------------------------------
// preDeployDriftCheck
// ---------------------------------------------------------------------------

// writePulledYaml writes the pulled-app yaml that BuildServerStateForDiff
// would render for raw. The local file matches what the server-state
// renderer produces, so the diff is byte-for-byte clean.
func writePulledYaml(t *testing.T, dir string, raw map[string]any, cid, aid string) string {
	t.Helper()
	state := apps.BuildServerStateForDiff(raw, cid, aid, nil, nil, nil, nil, nil)
	bytes, err := yaml.Marshal(state)
	if err != nil {
		t.Fatalf("marshal yaml: %v", err)
	}
	path := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(path, bytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func TestPreDeployDriftCheck_SkipsWhenYamlMissingIdCidAid(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte("app: hello\nport: 3000\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{AccountID: "acc-1"}
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false); err != nil {
		t.Errorf("expected nil (gate skipped on fresh deploy yaml), got: %v", err)
	}
}

func TestPreDeployDriftCheck_SkipsWhenYamlUnparseable(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	// Garbage: not valid yaml. preDeployDriftCheck should fall through silently.
	if err := os.WriteFile(yamlPath, []byte("\t\t::not-yaml::\nkey: [unclosed\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &config.Config{AccountID: "acc-1"}
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false); err != nil {
		t.Errorf("expected nil (gate skipped on unparseable yaml), got: %v", err)
	}
}

func TestPreDeployDriftCheck_NoDriftReturnsNil(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}
	yamlPath := writePulledYaml(t, dir, raw, "k1", "acc-1")

	srv := fakeConductorForDeploy(t, raw, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false); err != nil {
		t.Errorf("expected nil for in-sync state, got: %v", err)
	}
}

func TestPreDeployDriftCheck_RefusesOnDrift(t *testing.T) {
	dir := t.TempDir()
	// Local says replicas=1.
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	// Server says replicas=5, divergent (not just additive).
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false)
	if err == nil {
		t.Fatal("expected drift refusal")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Errorf("error should mention drift; got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force as the bypass; got: %v", err)
	}
}

// Regression for issue 86: `runos deploy --json` on drift wrote the
// human-readable drift report (boxes, --- local/+++ server, reconcile
// hints) to stdout before the JSON envelope, breaking jq parsers. Under
// --json the refusal output must land on stderr so stdout stays a clean
// JSON stream. Validates by capturing stdout while the gate runs and
// confirming nothing was written to it.
func TestPreDeployDriftCheck_JsonMode_KeepsStdoutClean(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	// Redirect os.Stdout to a pipe so we can read what the gate wrote.
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, true /* jsonOutput */)
	w.Close()
	if err == nil {
		t.Fatal("expected drift refusal")
	}

	var buf strings.Builder
	io.Copy(&buf, r)
	if buf.Len() != 0 {
		t.Errorf("--json mode wrote %d bytes to stdout (should be silent):\n%s", buf.Len(), buf.String())
	}
}

func TestPreDeployDriftCheck_LocalSupersetPassesThrough(t *testing.T) {
	// User added new fields locally (the "I edited metricsPort into my
	// yaml and want to deploy" flow). Local is a strict superset of
	// server: nothing on the server gets cleared, deploy just sets
	// the new fields. The gate must allow this without --force.
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")

	// Add metricsPort + metricsPath to local — fields the server
	// doesn't have. Local is now a strict superset of server.
	localBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	withMetrics := string(localBytes) + "metricsPort: 3000\nmetricsPath: /metrics\n"
	if err := os.WriteFile(yamlPath, []byte(withMetrics), 0644); err != nil {
		t.Fatalf("rewrite yaml: %v", err)
	}

	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false); err != nil {
		t.Errorf("expected nil (local-superset is the user's intended push), got: %v", err)
	}
}

// I2-4c regression: the warning splits server-only fields by
// clear-on-omit semantics. clusterDomainId is preserve-on-omit, so
// listing it must NOT trigger the "WILL be cleared" copy or the
// "apps pull --force" recovery hint. Both belong to the clear case.
// The field still gets enumerated under the preserve bucket.
func TestPreDeployDriftCheck_ForceServerOnly_PreserveOnly(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":              "ab12c",
		"name":            "web",
		"replicas":        float64(1),
		"clusterDomainId": "elpfn",
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	stderr := captureStderr(t, func() {
		cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
		if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, true, false, false); err != nil {
			t.Errorf("expected nil with --force, got: %v", err)
		}
	})

	if !strings.Contains(stderr, "Note: the server has fields your local yaml doesn't") {
		t.Errorf("expected the note header; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, `clusterDomainId ("elpfn")`) {
		t.Errorf("expected enumeration of clusterDomainId; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Preserved server-side") {
		t.Errorf("expected preserve bucket header; got:\n%s", stderr)
	}
	if strings.Contains(stderr, "WILL be cleared") {
		t.Errorf("preserve-only fields must NOT trigger the cleared-banner; got:\n%s", stderr)
	}
	if strings.Contains(stderr, "runos apps pull") {
		t.Errorf("apps-pull recovery hint should only appear when clear fields are at risk; got:\n%s", stderr)
	}
}

// I2-4c partner: when an actual clear-on-omit field (healthCheckPath)
// drifts server-only, the CLEARED bucket fires AND the apps-pull
// recovery hint shows up. Pre-fix, the same warning text fired
// regardless of which fields were in scope; this test pins both halves.
func TestPreDeployDriftCheck_ForceServerOnly_ClearFieldFires(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":              "ab12c",
		"name":            "web",
		"replicas":        float64(1),
		"healthCheckPath": "/healthz",
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	stderr := captureStderr(t, func() {
		cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
		if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, true, false, false); err != nil {
			t.Errorf("expected nil with --force, got: %v", err)
		}
	})

	if !strings.Contains(stderr, "WILL be cleared") {
		t.Errorf("expected cleared-banner for healthCheckPath; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, `healthCheckPath ("/healthz")`) {
		t.Errorf("expected enumeration of healthCheckPath; got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "runos apps pull") {
		t.Errorf("expected apps-pull recovery hint; got:\n%s", stderr)
	}
}

// captureStderr is the os.Stderr counterpart to captureStdout above.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stderr = old
	return <-done
}

func TestPreDeployDriftCheck_LegacyEmitsMigrationHint(t *testing.T) {
	dir := t.TempDir()
	// Local says replicas=1 (drift target), divergent vs server.
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	// Capture stdout to verify the migration block appears in the
	// refusal output when hasLegacy is true.
	out := captureStdout(t, func() {
		cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
		if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, true, false); err == nil {
			t.Error("expected gate to refuse on drift, got nil")
		}
	})

	// I2-4d regression: must mention top-level shorthand fields (NOT
	// "deprecated", which contradicted the docs for `domain:`) and the
	// apps-pull migration command.
	if !strings.Contains(out, "top-level shorthand fields") {
		t.Errorf("expected output to mention 'top-level shorthand fields'; got:\n%s", out)
	}
	if strings.Contains(out, "deprecated field names") {
		t.Errorf("output must not call the shorthand 'deprecated' (I2-4d regression); got:\n%s", out)
	}
	if !strings.Contains(out, "runos apps pull") {
		t.Errorf("expected output to recommend 'runos apps pull'; got:\n%s", out)
	}
	if !strings.Contains(out, "RECOMMENDED") {
		t.Errorf("expected output to flag the migration as RECOMMENDED so the LLM picks it; got:\n%s", out)
	}
	// Must NOT lead with --force. The "Deploy anyway" hint should still be
	// present (so power users can bypass) but framed as an "Other options"
	// fallback.
	recommendedIdx := strings.Index(out, "RECOMMENDED")
	deployForceIdx := strings.Index(out, "runos deploy --force")
	if deployForceIdx >= 0 && deployForceIdx < recommendedIdx {
		t.Errorf("--force suggestion should appear AFTER the RECOMMENDED migration block; got:\n%s", out)
	}
}

func TestPreDeployDriftCheck_NonLegacyKeepsStandardOutput(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	out := captureStdout(t, func() {
		cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
		_ = preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false)
	})

	// Standard refusal: no migration messaging.
	if strings.Contains(out, "deprecated field names") || strings.Contains(out, "RECOMMENDED") {
		t.Errorf("standard refusal should not mention deprecated fields or RECOMMENDED; got:\n%s", out)
	}
	if !strings.Contains(out, "Reconcile:") {
		t.Errorf("standard refusal should still suggest Reconcile:; got:\n%s", out)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was written. Used to assert on output that the deploy gate
// prints (rather than putting in the returned error).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestPreDeployDriftCheck_ForceBypassesGate(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	srv := fakeConductorForDeploy(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, true, false, false); err != nil {
		t.Errorf("expected nil with --force despite drift; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// sourceVersionFromPrepare, picks the id to record post-upload
// ---------------------------------------------------------------------------

func TestSourceVersionFromPrepare(t *testing.T) {
	tests := []struct {
		name string
		in   *deploy.PrepareResponse
		want string
	}{
		{
			name: "prefers CliUploadID when both are set",
			in:   &deploy.PrepareResponse{JobID: "job-x", CliUploadID: "upload-y"},
			want: "upload-y",
		},
		{
			name: "falls back to JobID for older conductor",
			in:   &deploy.PrepareResponse{JobID: "job-x"},
			want: "job-x",
		},
		{
			name: "empty when neither is set",
			in:   &deploy.PrepareResponse{},
			want: "",
		},
		{
			name: "nil response returns empty",
			in:   nil,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceVersionFromPrepare(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// preDeployCodeDriftCheck, sidecar-vs-server cliUploadID comparison
// ---------------------------------------------------------------------------

// fakeArchivesEndpoint stands up an httptest.Server that serves
// ListCliArchives (path suffix /cli-archives) and returns an empty app
// payload for any other path so LoadLocalApp's downstream callers don't
// blow up. The yaml itself is loaded via apps.LoadLocalApp before the
// gate makes any HTTP call.
func fakeArchivesEndpoint(t *testing.T, archives []apps.CliArchive) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if strings.HasSuffix(r.URL.Path, "/cli-archives") {
			// Match the conductor's 9.0.0+ envelope shape that
			// ListCliArchives now expects (I8-K).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cid":      "test-cid",
				"appId":    "test-id",
				"appName":  "test",
				"archives": archives,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPreDeployCodeDriftCheck_NoSidecarSkips(t *testing.T) {
	dir := t.TempDir()
	// Yaml is shaped right but no sidecar exists.
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")

	// Server should never be hit, but provide one anyway.
	srv := fakeArchivesEndpoint(t, []apps.CliArchive{{CliUploadID: "newer", PushTime: "2026-04-28T10:00:00Z"}})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, false, false); err != nil {
		t.Errorf("expected nil (no baseline → skip), got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_NoNewerArchivesReturnsNil(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	srv := fakeArchivesEndpoint(t, []apps.CliArchive{
		{CliUploadID: "v0", PushTime: "2026-04-26T10:00:00Z"},
		{CliUploadID: "v1", PushTime: "2026-04-27T10:00:00Z"}, // recorded
	})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, false, false); err != nil {
		t.Errorf("expected nil (no newer archives), got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_RefusesWhenNewerArchivesExist(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	srv := fakeArchivesEndpoint(t, []apps.CliArchive{
		{CliUploadID: "v1", PushTime: "2026-04-27T10:00:00Z"}, // recorded
		{CliUploadID: "v2", PushTime: "2026-04-27T11:00:00Z"}, // newer
		{CliUploadID: "v3", PushTime: "2026-04-28T09:00:00Z"}, // newer
	})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, false, false)
	if err == nil {
		t.Fatal("expected refusal when server has newer deploys")
	}
	if !strings.Contains(err.Error(), "code drift") {
		t.Errorf("error should mention code drift; got: %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force; got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_ForceBypasses(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	srv := fakeArchivesEndpoint(t, []apps.CliArchive{
		{CliUploadID: "v1", PushTime: "2026-04-27T10:00:00Z"},
		{CliUploadID: "v2", PushTime: "2026-04-27T11:00:00Z"},
	})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, true, false); err != nil {
		t.Errorf("expected nil with --force despite newer archives; got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_RecordedIdNotInListRefusesWithoutForce(t *testing.T) {
	// Pre-fix the gate silently skipped when the recorded source version
	// wasn't in the archive list, which made it trivially bypassable
	// (hand-edit `.source-version` to garbage and the gate goes silent).
	// The fix refuses without --force; --force is the explicit consent
	// path to deploy local source as a fresh archive.
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	// Sidecar references a cliUploadID the server no longer reports.
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "purged-id"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	srv := fakeArchivesEndpoint(t, []apps.CliArchive{
		{CliUploadID: "v2", PushTime: "2026-04-27T11:00:00Z"},
	})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, false, false); err == nil {
		t.Errorf("expected refusal when recorded id isn't in archive list (without --force)")
	} else if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal should mention --force, got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_RecordedIdNotInListProceedsWithForce(t *testing.T) {
	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "purged-id"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	srv := fakeArchivesEndpoint(t, []apps.CliArchive{
		{CliUploadID: "v2", PushTime: "2026-04-27T11:00:00Z"},
	})
	t.Setenv("RUNOS_API_URL", srv.URL)

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, true, false); err != nil {
		t.Errorf("expected --force to bypass the recorded-id-missing gate; got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_FetchFailureRefusesWithoutForce(t *testing.T) {
	// Code drift is the deploy payload: the deploy itself does not recheck
	// for upstream archives, so a fail-open here would silently let through
	// the very thing this gate exists to prevent. Refuse without --force.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	t.Setenv("RUNOS_API_URL", srv.URL)

	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, false, false)
	if err == nil {
		t.Fatal("expected error when archive listing fails without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force, got: %v", err)
	}
}

func TestPreDeployCodeDriftCheck_FetchFailureProceedsWithForce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	t.Setenv("RUNOS_API_URL", srv.URL)

	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")
	if err := apps.WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	if err := preDeployCodeDriftCheck(cfg, "tok", "k1", yamlPath, true, false); err != nil {
		t.Errorf("expected nil with --force on fetch failure, got: %v", err)
	}
}

func TestPreDeployDriftCheck_FetchFailureWarnsButProceeds(t *testing.T) {
	// Server returns 500 on every endpoint, simulates a transient API issue.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	t.Setenv("RUNOS_API_URL", srv.URL)

	dir := t.TempDir()
	yamlPath := writePulledYaml(t, dir, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1")

	cfg := &config.Config{AccountID: "acc-1", ConductorURL: srv.URL}
	// Even though fetch fails, the gate should not block, the deploy
	// will fail loudly later if the API is genuinely unreachable.
	if err := preDeployDriftCheck(cfg, "tok", "k1", yamlPath, false, false, false); err != nil {
		t.Errorf("expected nil (warn-and-proceed on fetch failure), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// syncConfigFromPrepareResponse
// ---------------------------------------------------------------------------

// TestSyncConfigFromPrepareResponse_StripsClassOnceServiceIdAssigned pins
// the post-deploy cleanup: class is creation-shorthand, the server reads
// it on first-create and ignores it forever after. Once the requires
// entry has an id (i.e. the service exists), the local yaml has no
// reason to keep class around. Strip it on writeback so re-pulls and
// re-deploys produce a clean yaml.
func TestSyncConfigFromPrepareResponse_StripsClassOnceServiceIdAssigned(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runos.yaml")

	cfg := &deploy.DeployConfig{
		App: "bedtime",
		ServicePortMappings: []deploy.ServicePortMapping{
			{Port: 3000, StandardHttps: boolPtr(true)},
		},
		Requires: map[string]deploy.ServiceRequirement{
			"bedtime-db": {
				Type:  "postgresql",
				Class: "postgresql.c0.beff",
				Config: map[string]any{
					"databaseName":     "bedtime",
					"databaseUsername": "bedtime",
				},
				Env: map[string]string{"url": "DATABASE_URL"},
			},
		},
	}
	if err := deploy.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	prep := &deploy.PrepareResponse{
		AppID: "u3yhf",
		Services: []deploy.ProvisionedService{
			{Alias: "bedtime-db", ID: "latnu", Type: "postgresql", IsNew: true},
		},
	}
	if err := syncConfigFromPrepareResponse(cfg, configPath, prep, "mycluster3", "myacct"); err != nil {
		t.Fatalf("syncConfigFromPrepareResponse: %v", err)
	}

	got := cfg.Requires["bedtime-db"]
	if got.ID != "latnu" {
		t.Errorf("ID should be populated; got %+v", got)
	}
	if got.Class != "" {
		t.Errorf("class should be stripped after id is assigned; got %q", got.Class)
	}
	// Config and env are server-authoritative but kept in the writeback
	// path so the subsequent yaml round-trip stays clean.
	if got.Config["databaseName"] != "bedtime" {
		t.Errorf("config should survive class stripping; got %+v", got.Config)
	}
	if got.Env["url"] != "DATABASE_URL" {
		t.Errorf("env should survive class stripping; got %+v", got.Env)
	}

	// On disk: class line gone, every other field intact.
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	if strings.Contains(string(raw), "class:") {
		t.Errorf("yaml on disk should not contain class: after writeback; got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "id: latnu") {
		t.Errorf("yaml on disk should contain id: latnu; got:\n%s", raw)
	}
}

// TestSyncConfigFromPrepareResponse_StripsLeftoverClassOnReDeploy covers
// the case where a previously-deployed app's yaml still has class set
// (e.g. user pulled before class-stripping landed, or wrote it manually).
// A re-deploy where the requires entry already has an id should also
// clear the field so the persisted yaml stays in good shape.
func TestSyncConfigFromPrepareResponse_StripsLeftoverClassOnReDeploy(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runos.yaml")

	cfg := &deploy.DeployConfig{
		App: "bedtime",
		ID:  "u3yhf",
		ServicePortMappings: []deploy.ServicePortMapping{
			{Port: 3000, StandardHttps: boolPtr(true)},
		},
		Requires: map[string]deploy.ServiceRequirement{
			"bedtime-db": {
				ID:    "latnu",
				Type:  "postgresql",
				Class: "postgresql.c0.beff", // leftover from earlier
			},
		},
	}
	if err := deploy.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("seed yaml: %v", err)
	}

	// Re-deploy: prepResp.Services may be empty (no new services to
	// provision) yet the writeback still walks Requires and cleans up
	// the leftover class.
	prep := &deploy.PrepareResponse{AppID: "u3yhf", Services: nil}
	if err := syncConfigFromPrepareResponse(cfg, configPath, prep, "mycluster3", "myacct"); err != nil {
		t.Fatalf("syncConfigFromPrepareResponse: %v", err)
	}

	if cfg.Requires["bedtime-db"].Class != "" {
		t.Errorf("leftover class should be stripped on re-deploy; got %q", cfg.Requires["bedtime-db"].Class)
	}
}

// boolPtr returns a pointer to b for the StandardHttps field which
// uses a *bool to disambiguate "false set explicitly" from "not set".
func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// mergeServerEnvIntoLocalFile — pre-deploy pull semantics
// ---------------------------------------------------------------------------
//
// The merge is local-wins (so a user's deploy-time edit isn't clobbered) and
// additive on missing server keys (so a teammate's console-set env shows up
// locally). These tests lock in each scenario the deploy flow walks through.

// readEnvForMergeTest parses the merged env file back into a map for assertion.
func readEnvForMergeTest(t *testing.T, path string) map[string]string {
	t.Helper()
	got, err := deploy.LoadEnvFile(path)
	if err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if got == nil {
		got = map[string]string{}
	}
	return got
}

// User edited APP_VERSION locally (the dominant deploy path). Local must be
// preserved verbatim so the deploy that follows pushes the new value up.
// Pre-fix, this case errored out as a "conflict" and printed a misleading
// "pre-deploy sync failed" warning.
func TestMergeServerEnvIntoLocalFile_LocalWinsOnDivergentValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("APP_VERSION=0.1.3\n"), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}

	missing, err := mergeServerEnvIntoLocalFile(path, map[string]string{"APP_VERSION": "0.1.2"}, ".env", "env vars")
	if err != nil {
		t.Fatalf("merge errored on a value mismatch (should be a no-op now): %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing should be empty (no server-only keys), got %v", missing)
	}
	got := readEnvForMergeTest(t, path)
	if got["APP_VERSION"] != "0.1.3" {
		t.Errorf("local edit was clobbered: got APP_VERSION=%q, want 0.1.3", got["APP_VERSION"])
	}
}

// Server-only keys (teammate added LOG_LEVEL via console) get pulled DOWN
// into the local file so the next deploy ships them. `missing` reports the
// pulled keys so the post-deploy `warnLocalDeletions` can flag the
// genuinely-surprising case-E (user *deleted* a key, deploy re-added it).
func TestMergeServerEnvIntoLocalFile_AdditiveOnServerOnlyKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("APP_VERSION=0.1.2\n"), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}

	server := map[string]string{"APP_VERSION": "0.1.2", "LOG_LEVEL": "debug"}
	missing, err := mergeServerEnvIntoLocalFile(path, server, ".env", "env vars")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(missing) != 1 || missing[0] != "LOG_LEVEL" {
		t.Errorf("missing should be [LOG_LEVEL], got %v", missing)
	}
	got := readEnvForMergeTest(t, path)
	if got["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL should be merged into local, got %q", got["LOG_LEVEL"])
	}
	if got["APP_VERSION"] != "0.1.2" {
		t.Errorf("APP_VERSION preserved, got %q", got["APP_VERSION"])
	}
}

// Local file matches server exactly — the merge is a no-op and we should NOT
// touch the file (mtime preservation, no spurious git diff on every deploy).
func TestMergeServerEnvIntoLocalFile_NoOpWhenLocalMatchesServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("APP_VERSION=0.1.2\n"), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mtimeBefore := statBefore.ModTime()

	if _, err := mergeServerEnvIntoLocalFile(path, map[string]string{"APP_VERSION": "0.1.2"}, ".env", "env vars"); err != nil {
		t.Fatalf("merge: %v", err)
	}

	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !statAfter.ModTime().Equal(mtimeBefore) {
		t.Errorf("file mtime changed despite no-op merge (would create spurious git diffs)")
	}
}

// Regression test for I2-4e' / I2-4e”' (TEST_LOG.md): the
// destructive-removal WARNING block must print whenever a custom-
// domain removal is detected, regardless of --force / --yes / non-tty.
// Round-6 also pins the source endpoint as /:aid/domains rather than
// /:aid/:cid/apps/:id/network-access (round-5 regression).
func TestPreDeployDomainRemovalGate_ForceStillSurfacesWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]deploy.Domain{
			{
				ID:               "d1",
				Fqdn:             "going.example.com",
				TargetIngressURL: "http://app-abc12.app-abc12.svc.cluster.local:3000",
			},
		})
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "tok", "mycluster2", "aid")
	c := &deploy.DeployConfig{ID: "abc12"}

	stderr := captureStderr(t, func() {
		// Force=true, skipPrompt=true: we must still see the warning,
		// not get a silent skip.
		if err := preDeployDomainRemovalGate(svc, c, true, true); err != nil {
			t.Fatalf("expected nil with --force, got: %v", err)
		}
	})

	for _, want := range []string{
		"WARNING: this deploy will REMOVE",
		"going.example.com",
		"--force passed; proceeding without prompt.",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q, got:\n%s", want, stderr)
		}
	}
}

// I2-4e' partner: when no removal is detected, the gate stays silent
// regardless of flags.
func TestPreDeployDomainRemovalGate_NoRemovalNoOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Server has the same domain the local yaml declares.
		_ = json.NewEncoder(w).Encode([]deploy.Domain{
			{
				ID:               "d1",
				Fqdn:             "kept.example.com",
				TargetIngressURL: "http://app-abc12.app-abc12.svc.cluster.local:3000",
			},
		})
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "tok", "mycluster2", "aid")
	c := &deploy.DeployConfig{
		ID:     "abc12",
		Domain: deploy.StringOrSlice{"kept.example.com"},
	}

	stderr := captureStderr(t, func() {
		if err := preDeployDomainRemovalGate(svc, c, true, false); err != nil {
			t.Fatalf("got: %v", err)
		}
	})
	if strings.Contains(stderr, "WARNING") {
		t.Errorf("no removal but WARNING surfaced:\n%s", stderr)
	}
}

// Regression test for I2-1b (TEST_LOG.md): writeProvisionedServiceYAMLs
// must route a 404 from services.Pull into the deferred bucket, not
// failed. The 404 happens on first-deploy of a brand-new
// requires.<alias>.class service because conductor's create work runs
// async; the show endpoint becomes 200 only after the deploy job
// progresses. The retry hook (--follow path) covers the actual write.
// Here we pin the classification logic so a future refactor can't
// regress it back to the alarming "Warning: write service yamls (some
// failed)" surface for the very common first-deploy path.
func TestIsAPINotFound_Typed404(t *testing.T) {
	apiErr := &dynacmd.APIError{StatusCode: http.StatusNotFound, Body: []byte(`{"error":"Service not found"}`)}
	if !isAPINotFound(apiErr) {
		t.Fatalf("expected isAPINotFound to be true for typed 404")
	}
	wrapped := fmt.Errorf("fetch mysql/pug3s: %w", apiErr)
	if !isAPINotFound(wrapped) {
		t.Fatalf("expected isAPINotFound to unwrap and detect 404")
	}
}

func TestIsAPINotFound_Other(t *testing.T) {
	cases := []error{
		nil,
		errors.New("plain error"),
		&dynacmd.APIError{StatusCode: http.StatusInternalServerError, Body: []byte(`x`)},
		&dynacmd.APIError{StatusCode: http.StatusUnauthorized, Body: []byte(`x`)},
	}
	for _, e := range cases {
		if isAPINotFound(e) {
			t.Errorf("isAPINotFound(%v) = true, want false", e)
		}
	}
}

// I3-D regression: syncAppState (env-vars / secret-env-vars /
// dependencies) and stampSynthesizedResources (synthesized RRC) all
// fetch via the deploy.Service GET methods. On first deploy the
// AppDocument hasn't been minted yet so the conductor returns 404 +
// "App 'xxx' not found". The classifier must recognise both error
// shapes (deploy.APIError and dynacmd.APIError) so the suppressing
// branch fires and the user doesn't see three scary warnings on what
// is actually the happy path. Without this, the deploy looks like it's
// failing.
func TestIsAPINotFound_DeployAPIError(t *testing.T) {
	deployErr := &deploy.APIError{StatusCode: http.StatusNotFound, Body: []byte(`{"error":"App 'xxx' not found"}`)}
	if !isAPINotFound(deployErr) {
		t.Fatalf("expected isAPINotFound to recognise deploy.APIError 404")
	}
	wrapped := fmt.Errorf("fetch app: %w", deployErr)
	if !isAPINotFound(wrapped) {
		t.Fatalf("expected isAPINotFound to unwrap deploy.APIError")
	}
	other := &deploy.APIError{StatusCode: http.StatusInternalServerError, Body: []byte(`x`)}
	if isAPINotFound(other) {
		t.Fatalf("non-404 deploy.APIError must not classify as 404")
	}
}

// I3-A regression: writeDeployIaCArtifacts must honour the resolved
// envFilename. A user with explicit `env: plain.env` in their yaml
// already has plain.env on disk; the placeholder must NOT land at the
// canonical `runos.<cid>.<id>.config.env` path because the dead file
// would forever mismatch the user's actual env file and conductor
// would never pick it up.
func TestWriteDeployIaCArtifacts_HonoursExplicitEnvFilename(t *testing.T) {
	dir := t.TempDir()
	// User's explicit env file already on disk.
	explicitPath := filepath.Join(dir, "plain.env")
	if err := os.WriteFile(explicitPath, []byte("FOO=bar\n"), 0644); err != nil {
		t.Fatalf("seed plain.env: %v", err)
	}
	writeDeployIaCArtifacts(dir, "plain.env", os.Stdout)
	// Explicit file untouched (existence-check skipped the write).
	body, err := os.ReadFile(explicitPath)
	if err != nil {
		t.Fatalf("read plain.env: %v", err)
	}
	if string(body) != "FOO=bar\n" {
		t.Errorf("explicit env file mutated: %q", string(body))
	}
	// Dead canonical placeholder must NOT exist.
	canonical := filepath.Join(dir, "runos.k1.ab12c.config.env")
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Errorf("canonical placeholder should not be written when user has explicit env path: stat err=%v", err)
	}
}

// I3-A complement: when the user has no explicit `env:` field, the
// canonical placeholder is written as before. This is the fresh-deploy
// happy path; the function passes the canonical filename in directly.
func TestWriteDeployIaCArtifacts_WritesCanonicalWhenNoExplicit(t *testing.T) {
	dir := t.TempDir()
	canonicalLeaf := "runos.k1.ab12c.config.env"
	writeDeployIaCArtifacts(dir, canonicalLeaf, os.Stdout)
	canonical := filepath.Join(dir, canonicalLeaf)
	body, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("expected canonical placeholder to be written: %v", err)
	}
	if !strings.Contains(string(body), "ConfigMap-backed env vars") {
		t.Errorf("placeholder header missing from canonical placeholder: %q", string(body))
	}
}

// I3-A complement: empty envFilename short-circuits the placeholder
// write entirely (still writes .dockerignore). Catches a regression
// where the function would join "" and create a plain `configDir`
// path, mis-stat it, and emit a confusing warning.
func TestWriteDeployIaCArtifacts_EmptyEnvFilenameSkipsPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeDeployIaCArtifacts(dir, "", os.Stdout)
	// .dockerignore should still appear (separate from env placeholder).
	if _, err := os.Stat(filepath.Join(dir, ".dockerignore")); err != nil {
		t.Errorf(".dockerignore should be written even when envFilename is empty: %v", err)
	}
	// No env file should appear at any name.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".env") || strings.HasSuffix(e.Name(), ".config.env") {
			t.Errorf("unexpected env file written: %s", e.Name())
		}
	}
}

// Fresh checkout: local env file doesn't exist yet, server has env vars.
// Pull must materialise the file with server's values so the deploy body
// has customEnvVars populated. Without this, conductor would interpret a
// missing customEnvVars in the deploy body as "user wants empty ConfigMap"
// and wipe the server's env on every deploy from a fresh clone.
func TestMergeServerEnvIntoLocalFile_MaterializesFileOnFreshCheckout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// Note: file deliberately does NOT exist on disk.

	server := map[string]string{"APP_VERSION": "0.1.2", "LOG_LEVEL": "info"}
	missing, err := mergeServerEnvIntoLocalFile(path, server, ".env", "env vars")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// Fresh-checkout case: missing is empty by design (defensive — can't
	// retroactively claim "user deleted these" when the file never existed).
	if len(missing) != 0 {
		t.Errorf("missing should be empty on first materialisation, got %v", missing)
	}
	got := readEnvForMergeTest(t, path)
	if got["APP_VERSION"] != "0.1.2" || got["LOG_LEVEL"] != "info" {
		t.Errorf("file should be materialised with server values, got %v", got)
	}
}
