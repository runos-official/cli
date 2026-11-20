package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/deploy"
)

// TestHintScopedModeOn401Or403 is the colocated unit test for findings
// 1435's shared helper: only a 401 or 403, in either of this repo's
// typed API-error shapes, gets the hint appended; every other status
// and every other error type passes through unchanged.
func TestHintScopedModeOn401Or403(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantHint bool
	}{
		{"deploy.APIError 401", &deploy.APIError{StatusCode: 401}, true},
		{"deploy.APIError 403", &deploy.APIError{StatusCode: 403}, true},
		{"apps.APIError 403", &apps.APIError{StatusCode: 403}, true},
		{"deploy.APIError 404 unchanged", &deploy.APIError{StatusCode: 404}, false},
		{"deploy.APIError 500 unchanged", &deploy.APIError{StatusCode: 500}, false},
		{"untyped error unchanged", errors.New("boom"), false},
		{"nil unchanged", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hintScopedModeOn401Or403(tc.err)
			hasHint := got != nil && strings.Contains(got.Error(), "--scoped")
			if hasHint != tc.wantHint {
				t.Errorf("hintScopedModeOn401Or403(%v) = %v, want hint=%v", tc.err, got, tc.wantHint)
			}
			if tc.err == nil && got != nil {
				t.Errorf("expected nil unchanged, got %v", got)
			}
		})
	}
}

// TestRunDeploy_AppFlagRefusesNonVCSAppInDefaultMode is the
// failing-first test for finding 1434 instance 1 (repeat of
// 1327/1329): acceptance criterion 6's DEFAULT MODE clause is
// "exactly the behaviour before this story", so `runos deploy --app
// <id> --sha <sha>` without --scoped must still read the app's detail
// and refuse locally -- never dispatching POST /apps/:id/deploy --
// when the app is not VCS-shaped. This row fails against the code
// this cycle's predecessor commit shipped (it skipped the read
// unconditionally and dispatched VCS for any app).
func TestRunDeploy_AppFlagRefusesNonVCSAppInDefaultMode(t *testing.T) {
	const appID = "ab123"
	log := &deployRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/acc1/k1/apps/" + appID:
			w.Write([]byte(`{"id":"` + appID + `","deployType":"cli"}`))
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setupScopedDeployEnv(t, srv.URL)

	c := newDeployTestCmd()
	for flag, val := range map[string]string{"app": appID, "sha": strings.Repeat("a", 40), "cid": "k1"} {
		if err := c.Flags().Set(flag, val); err != nil {
			t.Fatalf("set %s flag: %v", flag, err)
		}
	}

	err := runDeploy(c, nil)
	if err == nil {
		t.Fatal("expected a local refusal for a non-VCS app, got nil")
	}
	if !strings.Contains(err.Error(), "only valid for VCS-deployed apps") {
		t.Errorf("error = %q, want the non-VCS refusal message", err.Error())
	}
	if log.hit("/acc1/k1/apps/" + appID + "/deploy") {
		t.Errorf("must never dispatch VCS deploy for a non-VCS app; saw %v", log.paths)
	}
}

// TestRunDeploy_DeployTypeInferenceToleratesLookupFailureInDefaultMode
// is the failing-first test for finding 1434 instance 2: in default
// mode, a failed or denied GetApp lookup while inferring deployType
// for a yaml that predates the field must NOT abort the deploy --
// deployType stays empty and the deploy proceeds down the CLI-upload
// path, exactly as it did before this story. This row fails against
// this cycle's predecessor commit, which turned any such failure into
// a hard refusal for every credential.
func TestRunDeploy_DeployTypeInferenceToleratesLookupFailureInDefaultMode(t *testing.T) {
	const appID = "ab123"
	log := &deployRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/acc1/k1/apps/"+appID:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal"}`))
		case strings.HasSuffix(r.URL.Path, "/prepare-cli-deployment"):
			w.Write([]byte(`{"jobId":"job-1","appId":"` + appID + `","uploadUrl":"/upload","token":"t","expiresAt":"2099-01-01T00:00:00Z"}`))
		case r.URL.Path == "/upload":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setupScopedDeployEnv(t, srv.URL)

	yamlPath := writeDeployFixture(t, "app: demo\nid: "+appID+"\nport: 3000\n")

	c := newDeployTestCmd()
	for flag, val := range map[string]string{"config": yamlPath, "cid": "k1"} {
		if err := c.Flags().Set(flag, val); err != nil {
			t.Fatalf("set %s flag: %v", flag, err)
		}
	}

	if err := runDeploy(c, nil); err != nil {
		t.Fatalf("runDeploy: %v (a failed deployType lookup must not abort a default-mode deploy)", err)
	}
	if !log.hit("/upload") {
		t.Errorf("expected the deploy to proceed to upload despite the failed lookup; saw %v", log.paths)
	}
}

// TestRunDeploy_DefaultModeBroadPrepareRefusalHintsScopedMode is the
// failing-first test for finding 1435: a 403 from the broad
// preparation route in default mode must name --scoped / RUNOS_SCOPED=1
// as the fix, since that is the one credential shape this refusal
// cannot otherwise explain itself to.
func TestRunDeploy_DefaultModeBroadPrepareRefusalHintsScopedMode(t *testing.T) {
	const appID = "ab123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/prepare-cli-deployment"):
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"authz.insufficient_grant"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setupScopedDeployEnv(t, srv.URL)

	yamlPath := writeDeployFixture(t, "app: demo\nid: "+appID+"\ndeployType: cli\nport: 3000\n")

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
	if !strings.Contains(err.Error(), "--scoped") {
		t.Errorf("error = %q, want the --scoped discovery hint", err.Error())
	}
}
