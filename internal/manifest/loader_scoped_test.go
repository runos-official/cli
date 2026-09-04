package manifest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/cache"
)

// FPL31 D11 and D12: the manifest is served per account, so the loader
// asks `/:aid/cli/manifest` first, falls back to the bare route on a 404
// only, and never hands back a manifest cached for another account
// without trying to refetch it first.

// fakeConductor serves the four manifest routes and records every path it
// was asked for, so a test can assert which route the loader chose rather
// than only what it got back.
type fakeConductor struct {
	mu     sync.Mutex
	paths  []string
	status map[string]int // per-path status override; 0 or absent means 200
	// scopedManifest is served on /{aid}/cli/manifest, bareManifest on
	// /cli/manifest, so a test can tell the two apart in the result.
	scopedVersion  string
	bareVersion    string
	scopedManifest string
	bareManifest   string
}

func newFakeConductor(aid string) (*fakeConductor, *httptest.Server) {
	f := &fakeConductor{
		status:         map[string]int{},
		scopedVersion:  "45.3.0+virt",
		bareVersion:    "45.3.0",
		scopedManifest: `{"version":"45.3.0+virt","commands":[{"command":"vms/list","endpoint":"/vms","method":"GET"}]}`,
		bareManifest:   `{"version":"45.3.0","commands":[{"command":"clusters/list","endpoint":"/clusters","method":"GET"}]}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		code := f.status[r.URL.Path]
		f.mu.Unlock()
		if code != 0 && code != http.StatusOK {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"error":"no"}`)
			return
		}
		switch r.URL.Path {
		case "/" + aid + versionEndpoint:
			writeVersion(w, f.scopedVersion)
		case versionEndpoint:
			writeVersion(w, f.bareVersion)
		case "/" + aid + manifestEndpoint:
			fmt.Fprint(w, f.scopedManifest)
		case manifestEndpoint:
			fmt.Fprint(w, f.bareManifest)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return f, httptest.NewServer(mux)
}

func writeVersion(w http.ResponseWriter, version string) {
	_ = json.NewEncoder(w).Encode(versionResponse{Version: version})
}

func (f *fakeConductor) refuse(path string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[path] = code
}

func (f *fakeConductor) served() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths)
}

func (f *fakeConductor) asked(path string) bool {
	return slices.Contains(f.served(), path)
}

// offlineConfig points config.Load at a temp HOME holding a real
// config.json, and clears the two env vars that would otherwise win.
//
// Both matter: config.Load reaches for the CDN when the file is missing
// and RUNOS_API_KEY is set, and auth refuses an explicitly empty
// RUNOS_API_KEY rather than falling through to the file. Neither belongs
// in a test that must not touch the network.
func offlineConfig(t *testing.T, accountID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	os.Unsetenv("RUNOS_API_KEY")
	os.Unsetenv("RUNOS_ACCOUNT_ID")
	if err := os.MkdirAll(filepath.Join(home, ".runos"), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fmt.Sprintf(`{"api_key":"pat-test-token","account_id":%q}`, accountID)
	if err := os.WriteFile(filepath.Join(home, ".runos", "config.json"), []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readSidecar(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, accountFileName))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	return string(data)
}

// seedCache writes a manifest, its sidecar and a FRESH version-check
// entry, which is the state a loader is in between two ordinary runs.
func seedCache(t *testing.T, dir, manifestJSON, accountID string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestFileName), []byte(manifestJSON), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, accountFileName), []byte(accountID), 0600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := cache.NewManager(dir).Set(versionCheckCacheKey, "seeded", versionCheckTTL); err != nil {
		t.Fatalf("seed version cache: %v", err)
	}
}

// Criterion 1. With an account id the loader calls the scoped routes and
// never the bare ones.
func TestScopedRoutesArePreferred(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Version != "45.3.0+virt" {
		t.Errorf("version = %q, want the scoped manifest's 45.3.0+virt", m.Version)
	}
	for _, want := range []string{"/" + aid + versionEndpoint, "/" + aid + manifestEndpoint} {
		if !fake.asked(want) {
			t.Errorf("loader never asked for %s; it asked %v", want, fake.served())
		}
	}
	for _, unwanted := range []string{versionEndpoint, manifestEndpoint} {
		if fake.asked(unwanted) {
			t.Errorf("loader fell back to the bare route %s with no reason to: %v", unwanted, fake.served())
		}
	}
	if got := readSidecar(t, dir); got != aid {
		t.Errorf("sidecar = %q, want %q", got, aid)
	}
}

// Criterion 2. An older conductor answers 404 on both scoped routes. The
// loader falls back and the caller sees a manifest, not an error.
func TestBothScopedRoutes404FallBackToTheBareOnes(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	fake.refuse("/"+aid+versionEndpoint, http.StatusNotFound)
	fake.refuse("/"+aid+manifestEndpoint, http.StatusNotFound)
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("an older conductor must still serve this CLI, got: %v", err)
	}
	if m.Version != "45.3.0" {
		t.Errorf("version = %q, want the bare manifest's 45.3.0", m.Version)
	}
	for _, want := range []string{versionEndpoint, manifestEndpoint} {
		if !fake.asked(want) {
			t.Errorf("loader never fell back to %s; it asked %v", want, fake.served())
		}
	}
}

// Criterion 3. The two routes ship independently, so each falls back on
// its own: a 404 on the scoped VERSION route must not stop the scoped
// manifest route being used.
func TestTheVersionRouteFallsBackOnItsOwn(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	fake.refuse("/"+aid+versionEndpoint, http.StatusNotFound)
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !fake.asked(versionEndpoint) {
		t.Errorf("the version route did not fall back: %v", fake.served())
	}
	if !fake.asked("/" + aid + manifestEndpoint) {
		t.Errorf("the manifest route must still be tried scoped-first: %v", fake.served())
	}
	if m.Version != "45.3.0+virt" {
		t.Errorf("version = %q, want the scoped manifest's 45.3.0+virt", m.Version)
	}
}

// Criterion 4. With no account id there is nothing to scope by, so the
// scoped route is never probed.
func TestNoAccountIDCallsTheBareRoutesOnly(t *testing.T) {
	offlineConfig(t, "")
	fake, srv := newFakeConductor("acct1")
	defer srv.Close()
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Version != "45.3.0" {
		t.Errorf("version = %q, want 45.3.0", m.Version)
	}
	for _, path := range fake.served() {
		if path != versionEndpoint && path != manifestEndpoint {
			t.Errorf("loader probed %s with no account id; it asked %v", path, fake.served())
		}
	}
	if got := readSidecar(t, dir); got != "" {
		t.Errorf("sidecar = %q, want empty for a bare-route manifest", got)
	}
}

// Criterion 5. A cached manifest belongs to the account it was served
// for. Pointing the CLI at another account must refetch, even with a
// fresh version-check entry that would otherwise skip the probe entirely.
func TestAnAccountSwitchRefetchesDespiteAFreshTTL(t *testing.T) {
	const aid = "acct2"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	dir := t.TempDir()
	seedCache(t, dir, `{"version":"45.3.0+virt","commands":[]}`, "acct1")

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !fake.asked("/" + aid + manifestEndpoint) {
		t.Errorf("an account switch must refetch, but the loader asked only %v", fake.served())
	}
	if len(m.Commands) != 1 || m.Commands[0].Command != "vms/list" {
		t.Errorf("returned the previous account's commands: %+v", m.Commands)
	}
	if got := readSidecar(t, dir); got != aid {
		t.Errorf("sidecar = %q, want it rewritten to %q", got, aid)
	}
}

// Criterion 6. A module toggle changes the version string through its
// semver build metadata (45.3.0 -> 45.3.0+virt). The loader compares the
// string and refetches; nothing here parses it.
func TestBuildMetadataOnTheVersionTriggersARefetch(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	dir := t.TempDir()
	// Cached at 45.3.0, and the version-check entry expired, so the probe
	// runs and sees 45.3.0+virt.
	seedCache(t, dir, `{"version":"45.3.0","commands":[]}`, aid)
	if err := cache.NewManager(dir).Set(versionCheckCacheKey, "45.3.0", -time.Hour); err != nil {
		t.Fatalf("expire version cache: %v", err)
	}

	m, err := NewLoader(srv.URL, dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !fake.asked("/" + aid + manifestEndpoint) {
		t.Errorf("a changed version must refetch the manifest; asked %v", fake.served())
	}
	if m.Version != "45.3.0+virt" {
		t.Errorf("version = %q, want 45.3.0+virt", m.Version)
	}
}

// Case 7. The CLI fails OPEN (FPL31 D18). An account mismatch the loader
// cannot resolve because conductor is unreachable returns the cached
// manifest rather than nothing; conductor refuses what it must, route by
// route.
func TestAnUnreachableConductorKeepsTheCachedManifest(t *testing.T) {
	offlineConfig(t, "acct2")
	_, srv := newFakeConductor("acct2")
	url := srv.URL
	srv.Close() // nothing is listening now
	dir := t.TempDir()
	seedCache(t, dir, `{"version":"45.3.0","commands":[{"command":"clusters/list"}]}`, "acct1")

	m, err := NewLoader(url, dir).Load()
	if err != nil {
		t.Fatalf("a mismatch plus an unreachable API must not fail: %v", err)
	}
	if m == nil || m.Version != "45.3.0" {
		t.Fatalf("expected the cached manifest back, got %+v", m)
	}
}

// Case 8. ForceUpdate is the manifest_update path, so it writes the
// sidecar too; otherwise a refresh would leave the account id behind.
func TestForceUpdateWritesTheSidecar(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).ForceUpdate()
	if err != nil {
		t.Fatalf("ForceUpdate: %v", err)
	}
	if m.Version != "45.3.0+virt" {
		t.Errorf("version = %q, want 45.3.0+virt", m.Version)
	}
	if !fake.asked("/" + aid + manifestEndpoint) {
		t.Errorf("ForceUpdate must go scoped-first: %v", fake.served())
	}
	if got := readSidecar(t, dir); got != aid {
		t.Errorf("sidecar = %q, want %q", got, aid)
	}
}

// Case 9. The fallback is for a conductor that never learned the route.
// A 500 says the route exists and failed, so retrying the bare route
// would hide a real fault behind an unfiltered command list.
func TestOnlyA404FallsBack(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	fake.refuse("/"+aid+versionEndpoint, http.StatusInternalServerError)
	fake.refuse("/"+aid+manifestEndpoint, http.StatusInternalServerError)
	dir := t.TempDir()

	if _, err := NewLoader(srv.URL, dir).Load(); err == nil {
		t.Fatal("a 500 with no cached manifest must be an error, not a silent fallback")
	}
	for _, unwanted := range []string{versionEndpoint, manifestEndpoint} {
		if fake.asked(unwanted) {
			t.Errorf("a 500 fell back to the bare route %s: %v", unwanted, fake.served())
		}
	}
}

// BareManifest is what tells "no such command" apart from "that module is
// off for this account", so it must ignore the account entirely and leave
// the cache alone.
func TestBareManifestIgnoresTheAccountAndWritesNothing(t *testing.T) {
	const aid = "acct1"
	offlineConfig(t, aid)
	fake, srv := newFakeConductor(aid)
	defer srv.Close()
	dir := t.TempDir()

	m, err := NewLoader(srv.URL, dir).BareManifest()
	if err != nil {
		t.Fatalf("BareManifest: %v", err)
	}
	if m.Version != "45.3.0" {
		t.Errorf("version = %q, want the unfiltered 45.3.0", m.Version)
	}
	if fake.asked("/" + aid + manifestEndpoint) {
		t.Errorf("BareManifest must not probe the scoped route: %v", fake.served())
	}
	if _, err := os.Stat(filepath.Join(dir, manifestFileName)); !os.IsNotExist(err) {
		t.Error("BareManifest wrote the manifest cache")
	}
	if _, err := os.Stat(filepath.Join(dir, accountFileName)); !os.IsNotExist(err) {
		t.Error("BareManifest wrote the account sidecar")
	}
}
