package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// TestPrepareDeployment_RouteSelection is the cmd-level half of story
// 346 finding 1352's requested test: for an existing app whose every
// requires entry already carries an id, prepareDeployment uses the
// app-scoped route; the moment one entry has no id (inline service
// creation, which that route cannot do for ANY credential), it falls
// back to the broad, cluster-scoped route instead -- asserted on the
// request log, not just on NeedsBroadPreparation's own pure return
// value (covered separately in internal/deploy).
func TestPrepareDeployment_RouteSelection(t *testing.T) {
	cases := []struct {
		name      string
		requires  map[string]deploy.ServiceRequirement
		wantPath  string
		forbidden string
	}{
		{
			name: "every requires entry has an id: app-scoped route",
			requires: map[string]deploy.ServiceRequirement{
				"db": {ID: "svc-1", Type: "postgres"},
			},
			wantPath:  "/aid1/cid1/apps/app01/prepare-cli-deployment",
			forbidden: "/aid1/cid1/prepare-cli-deployment",
		},
		{
			name: "one requires entry has no id: falls back to the broad route",
			requires: map[string]deploy.ServiceRequirement{
				"db":    {ID: "svc-1", Type: "postgres"},
				"cache": {Type: "redis"},
			},
			wantPath:  "/aid1/cid1/prepare-cli-deployment",
			forbidden: "/aid1/cid1/apps/app01/prepare-cli-deployment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"jobId":"job-1","appId":"app01","uploadUrl":"/u","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
			}))
			t.Cleanup(srv.Close)

			svc := deploy.NewService(srv.URL, "t", "cid1", "aid1")
			cfg := &deploy.DeployConfig{App: "myapp", ID: "app01", Requires: tc.requires}

			if _, err := prepareDeployment(svc, cfg); err != nil {
				t.Fatalf("prepareDeployment: %v", err)
			}

			found := false
			for _, p := range paths {
				if p == tc.wantPath {
					found = true
				}
				if p == tc.forbidden {
					t.Errorf("hit the forbidden route %q; paths = %v", tc.forbidden, paths)
				}
			}
			if !found {
				t.Errorf("expected route %q to be hit; paths = %v", tc.wantPath, paths)
			}
		})
	}
}

// TestPrepareDeployment_RefusesMismatchedTarget pins findings
// 1328/1354 end to end through prepareDeployment: a response naming a
// different app than the one requested refuses before the caller
// (runDeploy) ever reaches syncConfigFromPrepareResponse, archive
// creation, or upload.
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
