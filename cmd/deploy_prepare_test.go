package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// TestPrepareDeployment_AlwaysUsesBroadRoute pins the current, reverted
// state of route selection (story 346, rework cycle 2): prepareDeployment
// always calls the broad, cluster-scoped preparation route. The
// app-scoped route (deploy.Service.PrepareExistingAppDeployment) is
// NOT automatically selected pending the change request raised for
// findings 1389/1408 -- the CLI has no way to learn its own
// credential's verified semantic type, so it cannot decide between
// the two routes without either breaking legacy deploys (findings
// 1326, 1352, 1389) or breaching D1 for a hypothetically scoped one
// (finding 1408). See that change request for the full evidence.
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

	if _, err := prepareDeployment(svc, cfg); err != nil {
		t.Fatalf("prepareDeployment: %v", err)
	}

	want := "/aid1/cid1/prepare-cli-deployment"
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("paths = %v, want exactly [%s]", paths, want)
	}
}

// TestPrepareDeployment_RefusesMismatchedTarget pins findings
// 1328/1354/1391 end to end through prepareDeployment: a response
// naming a different app than the one requested refuses before the
// caller (runDeploy) ever reaches syncConfigFromPrepareResponse,
// archive creation, or upload.
func TestPrepareDeployment_RefusesMismatchedTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","appId":"app02","uploadUrl":"/u","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
	cfg := &deploy.DeployConfig{App: "myapp", ID: "app01"}

	if _, err := prepareDeployment(svc, cfg); err == nil {
		t.Fatal("expected a refusal for a mismatched app id, got nil")
	}
}
