package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// newDeployTestCmd builds a cobra.Command with the same flag shape as
// the real deployCmd, without reusing the shared package-level
// variable (avoids cross-test flag-state leakage, matching
// newFollowTestCmd's rationale).
func newDeployTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "deploy"}
	c.Flags().StringP("config", "c", "runos.yaml", "")
	c.Flags().StringP("cid", "", "", "")
	c.Flags().BoolP("follow", "f", false, "")
	c.Flags().BoolP("json", "j", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().BoolP("yes", "y", false, "")
	c.Flags().String("app", "", "")
	c.Flags().String("sha", "", "")
	c.Flags().Bool("allow-dirty", false, "")
	c.Flags().StringArray("build-arg", nil, "")
	addScopedModeFlag(c)
	return c
}

// deployRequestLog is a thread-safe recorder of every path a test
// server saw, shared by every row of the story 346 criterion-6 native
// dispatch matrix below.
type deployRequestLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *deployRequestLog) record(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, path)
}

func (l *deployRequestLog) hit(path string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.paths {
		if p == path {
			return true
		}
	}
	return false
}

func (l *deployRequestLog) any(pred func(string) bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.paths {
		if pred(p) {
			return true
		}
	}
	return false
}

// setupScopedDeployEnv points config.Load, auth.ResolveToken and the
// deploy Service at a disposable, network-free fixture: a HOME under
// t.TempDir() with a config.json already on disk (so config.Load
// takes the "read from disk" branch and never calls FetchRemoteConfig
// over the network -- AI-first-go.md requires tests stay network-
// free), RUNOS_API_KEY for a zero-network token resolution, and
// RUNOS_API_URL pointed at the fixture server.
func setupScopedDeployEnv(t *testing.T, apiURL string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runosDir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(runosDir, 0755); err != nil {
		t.Fatalf("mkdir .runos: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runosDir, "config.json"), []byte(`{"account_id":"acc1"}`), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	t.Setenv("RUNOS_API_KEY", "test-key")
	t.Setenv("RUNOS_ACCOUNT_ID", "acc1")
	t.Setenv("RUNOS_API_URL", apiURL)
}

// writeDeployFixture writes a runos.yaml (given verbatim, caller's
// responsibility to shape it) plus the Dockerfile ResolveDockerfilePath
// requires to exist on disk, into a fresh temp directory, and returns
// the yaml's absolute path.
func writeDeployFixture(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write runos.yaml: %v", err)
	}
	return yamlPath
}

// isBroadPrepareRoute matches either spelling of the broad,
// cluster-scoped preparation route -- the one scoped mode must never
// reach (story 346 S2; D1).
func isBroadPrepareRoute(path string) bool {
	return path == "/acc1/k1/prepare-cli-deployment" || path == "/acc1/k1/prepareCliDeployment"
}

// isOutsideApplyDeployContract matches every read S5 names as
// outside the published apps.deploy contract: drift (cli-archives,
// app detail, env, secret files, overrides), the domain-removal gate,
// and syncAppState's dependency/env fetch.
func isOutsideApplyDeployContract(path string) bool {
	switch {
	case strings.Contains(path, "/cli-archives"):
		return true
	case strings.Contains(path, "/domains"):
		return true
	case strings.Contains(path, "/dependencies"):
		return true
	case strings.Contains(path, "/env-vars"):
		return true
	case strings.Contains(path, "/secret-env-vars"):
		return true
	case strings.Contains(path, "/networkAccess"):
		return true
	case path == "/acc1/k1/apps":
		return true
	// A bare app-detail GET, e.g. /acc1/k1/apps/ab123 -- but NOT the
	// prepare/deploy/upload POSTs this test also issues against paths
	// that start the same way.
	case strings.HasPrefix(path, "/acc1/k1/apps/") && !strings.Contains(path, "prepare-cli-deployment") && !strings.HasSuffix(path, "/deploy") && !strings.Contains(path, "/upload"):
		return true
	default:
		return false
	}
}

// TestRunDeploy_ScopedRefusalRows is the scoped-refusal third of story
// 346 acceptance criterion 6's native dispatch matrix, driven at the
// command entry point (runDeploy itself, via cobra): an unpinned app,
// a requires alias with no id, and a yaml with no explicit deployType
// each refuse locally in scoped mode, with ZERO requests to the
// fixture server. A server that answers any request at all fails the
// row outright (http.NotFound alone would let a mutation that forgot
// the early-return pass by accident).
func TestRunDeploy_ScopedRefusalRows(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		wantErr   string
		touchesID bool // true once id: is in the yaml, for the no-deployType row
	}{
		{
			name:    "unpinned app",
			yaml:    "app: demo\nport: 3000\n",
			wantErr: "scoped mode requires a pinned app",
		},
		{
			name:    "requires alias with no id",
			yaml:    "app: demo\nid: ab123\ndeployType: cli\nport: 3000\nrequires:\n  db:\n    type: postgres\n",
			wantErr: `"db" has none`,
		},
		{
			name:    "no explicit deployType",
			yaml:    "app: demo\nid: ab123\nport: 3000\n",
			wantErr: "scoped mode requires an explicit deployType",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &deployRequestLog{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.record(r.URL.Path)
				t.Errorf("scoped refusal row made a request to %s; want zero requests", r.URL.Path)
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)

			setupScopedDeployEnv(t, srv.URL)
			yamlPath := writeDeployFixture(t, tc.yaml)

			c := newDeployTestCmd()
			if err := c.Flags().Set("config", yamlPath); err != nil {
				t.Fatalf("set config flag: %v", err)
			}
			if err := c.Flags().Set("cid", "k1"); err != nil {
				t.Fatalf("set cid flag: %v", err)
			}
			if err := c.Flags().Set("scoped", "true"); err != nil {
				t.Fatalf("set scoped flag: %v", err)
			}

			err := runDeploy(c, nil)
			if err == nil {
				t.Fatal("expected a local refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
			if len(log.paths) != 0 {
				t.Errorf("request log = %v, want empty", log.paths)
			}
		})
	}
}

// scopedFixtureServer answers exactly the requests a successful
// scoped CLI-upload deploy needs (the app-scoped preparation route
// and the upload destination it names) and records every path seen,
// so a test can assert the complete allowed set and fail on anything
// outside it.
func scopedFixtureServer(t *testing.T, appID string) (*httptest.Server, *deployRequestLog) {
	t.Helper()
	log := &deployRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/apps/"+appID+"/prepare-cli-deployment"):
			w.Write([]byte(`{"jobId":"job-1","appId":"` + appID + `","uploadUrl":"/upload","token":"upload-tok","expiresAt":"2099-01-01T00:00:00Z"}`))
		case r.URL.Path == "/upload":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("scoped deploy made an unexpected request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

// TestRunDeploy_ScopedSuccessRowUsesOnlyTheAppScopedRouteAndUpload is
// the scoped-success third of the matrix: a pinned app with every
// requires entry already linked deploys fire-and-forget through the
// app-scoped route and the upload alone. S5's skipped safeguards mean
// no drift, domain, dependency or env request ever reaches the
// server, and S2 means the broad cluster-scoped route (either
// spelling) is never hit either.
func TestRunDeploy_ScopedSuccessRowUsesOnlyTheAppScopedRouteAndUpload(t *testing.T) {
	const appID = "ab123"
	srv, log := scopedFixtureServer(t, appID)
	setupScopedDeployEnv(t, srv.URL)

	yamlPath := writeDeployFixture(t, "app: demo\nid: "+appID+"\ndeployType: cli\nport: 3000\n"+
		"requires:\n  db:\n    id: svc-1\n    type: postgres\n")

	c := newDeployTestCmd()
	for flag, val := range map[string]string{"config": yamlPath, "cid": "k1", "scoped": "true"} {
		if err := c.Flags().Set(flag, val); err != nil {
			t.Fatalf("set %s flag: %v", flag, err)
		}
	}

	if err := runDeploy(c, nil); err != nil {
		t.Fatalf("runDeploy: %v", err)
	}

	wantHit := "/acc1/k1/apps/" + appID + "/prepare-cli-deployment"
	if !log.hit(wantHit) {
		t.Errorf("expected the app-scoped preparation route %q to be hit; saw %v", wantHit, log.paths)
	}
	if !log.hit("/upload") {
		t.Errorf("expected the upload destination to be hit; saw %v", log.paths)
	}
	if log.any(isBroadPrepareRoute) {
		t.Errorf("scoped deploy must never hit the broad cluster-scoped route; saw %v", log.paths)
	}
	if log.any(isOutsideApplyDeployContract) {
		t.Errorf("scoped deploy must never read outside the apps.deploy contract; saw %v", log.paths)
	}
}

// TestRunDeploy_ScopedRepeatDeploySucceeds pins finding 1390's exact
// repro end to end: a deploy-only credential redeploys the same
// checkout a second time, using the source record the first deploy
// wrote, and succeeds -- because S5 skips preDeployCodeDriftCheck
// (the GET /cli-archives read a deploy-only grant cannot make) in
// scoped mode, in both runs.
func TestRunDeploy_ScopedRepeatDeploySucceeds(t *testing.T) {
	const appID = "ab123"
	srv, log := scopedFixtureServer(t, appID)
	setupScopedDeployEnv(t, srv.URL)

	yamlPath := writeDeployFixture(t, "app: demo\nid: "+appID+"\ndeployType: cli\nport: 3000\n"+
		"requires:\n  db:\n    id: svc-1\n    type: postgres\n")

	for i := 0; i < 2; i++ {
		c := newDeployTestCmd()
		for flag, val := range map[string]string{"config": yamlPath, "cid": "k1", "scoped": "true"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		if err := runDeploy(c, nil); err != nil {
			t.Fatalf("runDeploy, attempt %d: %v", i+1, err)
		}
	}

	if log.any(func(p string) bool { return strings.Contains(p, "/cli-archives") }) {
		t.Errorf("a repeat scoped deploy must never read /cli-archives; saw %v", log.paths)
	}
}
