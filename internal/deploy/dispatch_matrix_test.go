package deploy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// requestLog is a thread-safe recorder of every path a test server saw,
// shared by the prepare/upload/VCS half of the story 346 criterion-6
// dispatch matrix (the follow half lives in internal/jobs's own
// progress_follow_test.go; together they cover every native deploy
// entry point named in the story's command inventory).
type requestLog struct {
	mu    sync.Mutex
	paths []string
}

func (r *requestLog) record(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
}

func (r *requestLog) hit(path string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.paths {
		if p == path {
			return true
		}
	}
	return false
}

// TestDeployDispatch_ExistingAppNeverUsesBroadClusterRoute is the
// prepare/VCS half of story 346 criterion 6's dispatch matrix: for an
// EXISTING app (deployConfig.ID already set, the shape cmd/deploy.go
// resolves before calling into this package) native dispatch MUST use
// the app-scoped preparation route for CLI uploads and the existing
// app-scoped VCS deploy route, and MUST NEVER reach the broad,
// cluster-scoped preparation route in either spelling
// (/prepare-cli-deployment or /prepareCliDeployment) -- that route
// stays reachable only from the separate new-app-creation path
// (PrepareDeployment, exercised by the second subtest below), which a
// minimal apps.deploy grant does not and should not cover.
func TestDeployDispatch_ExistingAppNeverUsesBroadClusterRoute(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/prepare-cli-deployment"):
			w.Write([]byte(`{"jobId":"job-1","appId":"app01","uploadUrl":"/u","token":"t","expiresAt":"2026-01-01T00:00:00Z"}`))
		case strings.HasSuffix(r.URL.Path, "/deploy"):
			w.Write([]byte(`{"jobId":"job-2"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := &Service{baseURL: srv.URL, httpClient: &http.Client{Timeout: 5 * time.Second}, token: "t", aid: "aid1", cid: "cid1"}

	cfg := &DeployConfig{App: "myapp", ID: "app01"}
	if _, err := svc.PrepareExistingAppDeployment("app01", cfg); err != nil {
		t.Fatalf("PrepareExistingAppDeployment: %v", err)
	}
	if _, err := svc.DeployVCS("app01", strings.Repeat("a", 40), "runos.yaml", nil); err != nil {
		t.Fatalf("DeployVCS: %v", err)
	}

	wantHit := "/aid1/cid1/apps/app01/prepare-cli-deployment"
	if !log.hit(wantHit) {
		t.Errorf("expected app-scoped preparation route %q to be hit; saw %v", wantHit, log.paths)
	}
	wantVCSHit := "/aid1/cid1/apps/app01/deploy"
	if !log.hit(wantVCSHit) {
		t.Errorf("expected VCS deploy route %q to be hit; saw %v", wantVCSHit, log.paths)
	}

	forbidden := []string{
		"/aid1/cid1/prepare-cli-deployment",
		"/aid1/cid1/prepareCliDeployment",
	}
	for _, f := range forbidden {
		if log.hit(f) {
			t.Errorf("broad cluster-scoped preparation route %q must never be hit for an existing app; saw %v", f, log.paths)
		}
	}
}

// TestDeployDispatch_NewAppUsesBroadRouteOnly documents the one
// deliberate exception: a deploy with no app id yet (first-time
// deploy, deployConfig.ID == "") has no existing app to scope to, so
// cmd/deploy.go calls the broad route. This is not a story 346
// violation -- D1 only forbids the broad route for an EXISTING app --
// but the matrix must still prove the broad route is reachable at all
// for that one legitimate case, and that it is the ONLY route hit.
func TestDeployDispatch_NewAppUsesBroadRouteOnly(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobId":"job-1","appId":"new-app","uploadUrl":"/u","token":"t","expiresAt":"2026-01-01T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)

	svc := &Service{baseURL: srv.URL, httpClient: &http.Client{Timeout: 5 * time.Second}, token: "t", aid: "aid1", cid: "cid1"}
	cfg := &DeployConfig{App: "myapp"} // no ID: brand-new app
	if _, err := svc.PrepareDeployment(cfg); err != nil {
		t.Fatalf("PrepareDeployment: %v", err)
	}

	want := "/aid1/cid1/prepare-cli-deployment"
	if len(log.paths) != 1 || log.paths[0] != want {
		t.Errorf("new-app prepare paths = %v, want exactly [%s]", log.paths, want)
	}
}

// TestUploadTarball_RefusesProtocolDowngrade pins finding 225: a
// conductor on https must never be able to steer the CLI's tarball
// upload (which carries the single-use upload bearer) to a plaintext
// URL. No request reaches the network at all; validateUploadURL
// refuses before dialing.
func TestUploadTarball_RefusesProtocolDowngrade(t *testing.T) {
	svc := &Service{baseURL: "https://conductor.example.com", httpClient: &http.Client{Timeout: 5 * time.Second}, token: "t"}
	err := svc.UploadTarball("http://evil.example.com/upload", "upload-bearer", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected a protocol-downgrade refusal, got nil")
	}
	if !strings.Contains(err.Error(), "refusing upload") {
		t.Errorf("error = %v, want a refusing-upload message", err)
	}
}

// TestUploadTarball_RefusesRedirect pins the second half of finding
// 225: even a same-scheme 3xx response is never followed, so the
// single-use upload bearer is never resent to a URL the conductor did
// not issue it for.
func TestUploadTarball_RefusesRedirect(t *testing.T) {
	var redirectTargetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	upload := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	t.Cleanup(upload.Close)

	svc := &Service{baseURL: upload.URL, httpClient: &http.Client{Timeout: 5 * time.Second}, token: "t"}
	err := svc.UploadTarball(upload.URL+"/upload", "upload-bearer", strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected a redirect refusal, got nil")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %v, want a redirect-refusal message", err)
	}
	if redirectTargetHits != 0 {
		t.Errorf("redirect target was hit %d times, want 0: the bearer must never be resent off the issued URL", redirectTargetHits)
	}
}

// TestUploadTarball_AcceptsMatchingScheme is the control case: an
// upload URL whose scheme matches (or exceeds) the conductor's own
// scheme succeeds, and the exact bearer is presented. Uses a plain
// (non-TLS) httptest server on both sides so the test exercises
// validateUploadURL's "same or better" rule without needing a trusted
// certificate; the https case is covered structurally by
// TestUploadTarball_RefusesProtocolDowngrade (an https base refuses an
// http target) -- the only remaining behavior to pin here is that a
// matching scheme is let through to a real request.
func TestUploadTarball_AcceptsMatchingScheme(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	svc := &Service{baseURL: srv.URL, httpClient: srv.Client(), token: "t"}
	if err := svc.UploadTarball(srv.URL+"/upload", "upload-bearer", strings.NewReader("data")); err != nil {
		t.Fatalf("UploadTarball: %v", err)
	}
	if gotAuth != "Bearer upload-bearer" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer upload-bearer")
	}
}
