package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/apps"
)

const hintSiteAppID = "ab123"

// pulledAppYAML is the id/cid/aid-carrying yaml shape preDeployDriftCheck
// and preDeployCodeDriftCheck need to treat configPath as a pulled-app
// manifest (so their gates actually run instead of skipping as fresh).
const pulledAppYAML = "app: demo\nid: " + hintSiteAppID + "\ncid: k1\naid: acc1\ndeployType: cli\nport: 3000\n"

// hintSiteServer builds a loopback recorder that answers `status` to
// exactly `failPath` and a safe, request-suppressing default to
// everything else (404 for GETs the various gates tolerate; the
// specific sidecar-triggered cli-archives path is handled by the
// caller when needed). Isolating the failure to one path is what lets
// the mutation check attribute a red row to exactly one
// hintScopedModeOn401Or403 call site.
func hintSiteServer(t *testing.T, failPath string, status int) (*httptest.Server, *deployRequestLog) {
	t.Helper()
	log := &deployRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == failPath {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"refused"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

// TestRunDeploy_DefaultModeHintSites is finding 1447's closure matrix:
// every default-mode-reachable deploy path that can surface a 401/403
// from a preflight read, one row per site, each isolated so only that
// site's hintScopedModeOn401Or403 call is exercised. Proven by
// mutation: removing any one wrap below (verified by hand before this
// commit, see the implementation summary) turns exactly its own row
// red, never another.
func TestRunDeploy_DefaultModeHintSites(t *testing.T) {
	t.Run("--app branch's app-detail read (terminal)", func(t *testing.T) {
		failPath := "/acc1/k1/apps/" + hintSiteAppID
		srv, log := hintSiteServer(t, failPath, http.StatusForbidden)
		setupScopedDeployEnv(t, srv.URL)

		c := newDeployTestCmd()
		for flag, val := range map[string]string{"app": hintSiteAppID, "sha": strings.Repeat("a", 40), "cid": "k1"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		err := runDeploy(c, nil)
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		assertHinted(t, err.Error())
		if log.hit("/acc1/k1/apps/" + hintSiteAppID + "/deploy") {
			t.Errorf("must never dispatch VCS deploy after this refusal; saw %v", log.paths)
		}
	})

	t.Run("preDeployDriftCheck's fail-open warning", func(t *testing.T) {
		failPath := "/acc1/k1/apps/" + hintSiteAppID
		srv, _ := hintSiteServer(t, failPath, http.StatusForbidden)
		setupScopedDeployEnv(t, srv.URL)
		yamlPath := writeDeployFixture(t, pulledAppYAML)

		c := newDeployTestCmd()
		for flag, val := range map[string]string{"config": yamlPath, "cid": "k1"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		stderr := captureStderr(t, func() { _ = runDeploy(c, nil) })
		assertHinted(t, stderr)
	})

	t.Run("preDeployCodeDriftCheck's --force fail-open warning", func(t *testing.T) {
		configDir := t.TempDir()
		if err := os.WriteFile(configDir+"/Dockerfile", []byte("FROM scratch\n"), 0644); err != nil {
			t.Fatalf("write Dockerfile: %v", err)
		}
		yamlPath := configDir + "/runos.yaml"
		// No cid/aid: preDeployDriftCheck's own skip ("fresh deploy
		// yaml") fires on CID=="" alone, so this row reaches ONLY
		// preDeployCodeDriftCheck (which skips on ID=="" alone, and ID
		// is set here) -- isolating the row to its one site.
		if err := os.WriteFile(yamlPath, []byte("app: demo\nid: "+hintSiteAppID+"\ndeployType: cli\nport: 3000\n"), 0644); err != nil {
			t.Fatalf("write runos.yaml: %v", err)
		}
		if err := apps.WriteSourceVersion(configDir, "k1", hintSiteAppID, "v1"); err != nil {
			t.Fatalf("write source version sidecar: %v", err)
		}

		failPath := "/acc1/k1/apps/" + hintSiteAppID + "/cli-archives"
		srv, _ := hintSiteServer(t, failPath, http.StatusForbidden)
		setupScopedDeployEnv(t, srv.URL)

		c := newDeployTestCmd()
		for flag, val := range map[string]string{"config": yamlPath, "cid": "k1", "force": "true"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		stderr := captureStderr(t, func() { _ = runDeploy(c, nil) })
		assertHinted(t, stderr)
	})

	syncAppStateRows := []struct {
		name     string
		failPath string
	}{
		{"syncAppState's dependencies read", "/acc1/k1/apps/" + hintSiteAppID + "/dependencies"},
		{"syncAppState's secret-env-vars read", "/acc1/k1/apps/" + hintSiteAppID + "/secret-env-vars"},
		{"syncAppState's env-vars read", "/acc1/k1/apps/" + hintSiteAppID + "/env-vars"},
	}
	for _, tc := range syncAppStateRows {
		t.Run(tc.name, func(t *testing.T) {
			// No cid/aid in the yaml: preDeployDriftCheck and
			// preDeployCodeDriftCheck both skip (fresh-deploy shape),
			// so only syncAppState's three reads run, isolating this
			// row to its own site.
			srv, _ := hintSiteServer(t, tc.failPath, http.StatusForbidden)
			setupScopedDeployEnv(t, srv.URL)
			yamlPath := writeDeployFixture(t, "app: demo\nid: "+hintSiteAppID+"\ndeployType: cli\nport: 3000\n")

			c := newDeployTestCmd()
			for flag, val := range map[string]string{"config": yamlPath, "cid": "k1"} {
				if err := c.Flags().Set(flag, val); err != nil {
					t.Fatalf("set %s flag: %v", flag, err)
				}
			}
			stderr := captureStderr(t, func() { _ = runDeploy(c, nil) })
			assertHinted(t, stderr)
		})
	}
}

// TestRunDeploy_HintNeverFiresOnWrongStatusOrScopedMode is the two
// control rows finding 1447 asks for, proving the hint is advice
// only: never for a status other than 401/403, and never once scoped
// mode has already declared itself.
func TestRunDeploy_HintNeverFiresOnWrongStatusOrScopedMode(t *testing.T) {
	t.Run("500 at the broad preparation route: no hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/prepare-cli-deployment") {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal"}`))
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		setupScopedDeployEnv(t, srv.URL)
		yamlPath := writeDeployFixture(t, "app: demo\nid: "+hintSiteAppID+"\ndeployType: cli\nport: 3000\n")

		c := newDeployTestCmd()
		for flag, val := range map[string]string{"config": yamlPath, "cid": "k1"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		err := runDeploy(c, nil)
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		if strings.Contains(err.Error(), "--scoped") {
			t.Errorf("error = %q, a 500 must never get the --scoped hint", err.Error())
		}
	})

	t.Run("403 at the app-scoped preparation route in scoped mode: no hint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/apps/"+hintSiteAppID+"/prepare-cli-deployment") {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"authz.insufficient_grant"}`))
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		setupScopedDeployEnv(t, srv.URL)
		yamlPath := writeDeployFixture(t, "app: demo\nid: "+hintSiteAppID+"\ndeployType: cli\nport: 3000\n"+
			"requires:\n  db:\n    id: svc-1\n    type: postgres\n")

		c := newDeployTestCmd()
		for flag, val := range map[string]string{"config": yamlPath, "cid": "k1", "scoped": "true"} {
			if err := c.Flags().Set(flag, val); err != nil {
				t.Fatalf("set %s flag: %v", flag, err)
			}
		}
		err := runDeploy(c, nil)
		if err == nil {
			t.Fatal("expected a refusal, got nil")
		}
		if strings.Contains(err.Error(), "--scoped") {
			t.Errorf("error = %q, scoped mode must never get the --scoped discovery hint (it already declared itself)", err.Error())
		}
	})
}

// assertHinted fails the test unless s names both the flag and the
// env var the hint promises.
func assertHinted(t *testing.T, s string) {
	t.Helper()
	if !strings.Contains(s, "--scoped") || !strings.Contains(s, "RUNOS_SCOPED=1") {
		t.Errorf("output %q does not contain the --scoped / RUNOS_SCOPED=1 hint", s)
	}
}
