package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// TestPrepareDeployment_AlwaysUsesBroadRoute pins default mode (story
// 346, acceptance criterion 6): with scoped mode not declared,
// prepareDeployment always calls the broad, cluster-scoped
// preparation route, matching behavior from before this story.
func TestPrepareDeployment_AlwaysUsesBroadRoute(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","appId":"app01","uploadUrl":"/u","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
	cfg := &deploy.DeployConfig{App: "myapp", ID: "app01", Requires: map[string]deploy.ServiceRequirement{
		"db": {ID: "svc-1", Type: "postgres"},
	}}

	if _, err := prepareDeployment(svc, cfg, false); err != nil {
		t.Fatalf("prepareDeployment: %v", err)
	}

	want := "/aid1/cid1/prepare-cli-deployment"
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("paths = %v, want exactly [%s]", paths, want)
	}
}

// TestPrepareDeployment_ScopedUsesAppScopedRouteOnly pins scoped mode
// (story 346, S2): prepareDeployment calls the app-scoped route only,
// never the broad cluster-scoped one in either spelling.
func TestPrepareDeployment_ScopedUsesAppScopedRouteOnly(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","appId":"app01","uploadUrl":"/u","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
	cfg := &deploy.DeployConfig{App: "myapp", ID: "app01", Requires: map[string]deploy.ServiceRequirement{
		"db": {ID: "svc-1", Type: "postgres"},
	}}

	if _, err := prepareDeployment(svc, cfg, true); err != nil {
		t.Fatalf("prepareDeployment: %v", err)
	}

	want := "/aid1/cid1/apps/app01/prepare-cli-deployment"
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("paths = %v, want exactly [%s]", paths, want)
	}
	for _, p := range paths {
		if p == "/aid1/cid1/prepare-cli-deployment" || p == "/aid1/cid1/prepareCliDeployment" {
			t.Errorf("scoped mode must never hit the broad cluster-scoped route; paths = %v", paths)
		}
	}
}

// TestPrepareDeployment_ScopedExplainsRefusal pins findings 1352/1408:
// a refusal from the app-scoped route in scoped mode is final (no
// fallback) and carries ExplainAppScopedPrepareRefusal's hint.
func TestPrepareDeployment_ScopedExplainsRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":"authz.cannot_reconfigure"}`))
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
	cfg := &deploy.DeployConfig{App: "myapp", ID: "app01", Requires: map[string]deploy.ServiceRequirement{
		"db": {ID: "svc-1", Type: "postgres", Class: "new-class"},
	}}

	_, err := prepareDeployment(svc, cfg, true)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "cannot resize or reconfigure") {
		t.Errorf("error = %v, want the app-scoped resize/reconfigure hint", err)
	}
}

// TestPrepareDeployment_RefusesMismatchedTarget pins findings
// 1328/1354/1391 end to end through prepareDeployment: a response
// naming a different app than the one requested refuses before the
// caller (runDeploy) ever reaches syncConfigFromPrepareResponse,
// archive creation, or upload. Applies in both modes; exercised here
// in default mode.
func TestPrepareDeployment_RefusesMismatchedTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","appId":"app02","uploadUrl":"/u","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
	cfg := &deploy.DeployConfig{App: "myapp", ID: "app01"}

	if _, err := prepareDeployment(svc, cfg, false); err == nil {
		t.Fatal("expected a refusal for a mismatched app id, got nil")
	}
}
