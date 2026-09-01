package apps

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testSidecarCID = "k1"
	testSidecarApp = "ab12c"
)

func TestReadSourceVersion_MissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if err != nil {
		t.Fatalf("ReadSourceVersion: %v", err)
	}
	if got != "" {
		t.Errorf("expected \"\" for missing file, got %q", got)
	}
}

func TestWriteSourceVersion_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := "9e2c1f0b-1234-4abc-9def-0123456789ab"
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, want); err != nil {
		t.Fatalf("WriteSourceVersion: %v", err)
	}
	got, err := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if err != nil {
		t.Fatalf("ReadSourceVersion: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// File should be plain text with a trailing newline.
	raw, err := os.ReadFile(SourceVersionPath(dir, testSidecarCID, testSidecarApp))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != want+"\n" {
		t.Errorf("file content = %q, want %q", raw, want+"\n")
	}
}

func TestWriteSourceVersion_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "first-id"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "second-id"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if got != "second-id" {
		t.Errorf("expected second-id after overwrite, got %q", got)
	}
}

func TestWriteSourceVersion_RejectsEmptyId(t *testing.T) {
	if err := WriteSourceVersion(t.TempDir(), testSidecarCID, testSidecarApp, ""); err == nil {
		t.Fatal("expected error for empty cliUploadID")
	}
}

func TestWriteSourceVersion_CreatesAppDir(t *testing.T) {
	parent := t.TempDir()
	appDir := filepath.Join(parent, "runos.k1.appid5")
	// appDir doesn't exist yet.
	if err := WriteSourceVersion(appDir, testSidecarCID, testSidecarApp, "abc"); err != nil {
		t.Fatalf("WriteSourceVersion: %v", err)
	}
	if _, err := os.Stat(appDir); err != nil {
		t.Errorf("appDir should have been created: %v", err)
	}
}

func TestReadSourceVersion_StripsTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	// File has whitespace + trailing newline; read should hand back the bare id.
	if err := os.WriteFile(SourceVersionPath(dir, testSidecarCID, testSidecarApp), []byte("  9e2c1f0b  \n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if err != nil {
		t.Fatalf("ReadSourceVersion: %v", err)
	}
	if got != "9e2c1f0b" {
		t.Errorf("got %q, want %q", got, "9e2c1f0b")
	}
}

// ---------------------------------------------------------------------------
// Per-app vs legacy sidecar handling
// ---------------------------------------------------------------------------

// TestReadSourceVersion_PrefersPerApp asserts that when both files exist
// (e.g. an upgraded project that has been re-deployed once) the per-app
// file wins. Two apps in one directory must not see each other's anchors.
func TestReadSourceVersion_PrefersPerApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(LegacySourceVersionPath(dir), []byte("legacy-id\n"), 0644); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if err := os.WriteFile(SourceVersionPath(dir, testSidecarCID, testSidecarApp), []byte("per-app-id\n"), 0644); err != nil {
		t.Fatalf("seed per-app: %v", err)
	}
	got, err := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if err != nil {
		t.Fatalf("ReadSourceVersion: %v", err)
	}
	if got != "per-app-id" {
		t.Errorf("got %q, want %q (per-app file must win when both exist)", got, "per-app-id")
	}
}

// TestReadSourceVersion_LegacyFallback covers single-app projects that
// were pulled before the per-app sidecar landed. Without the fallback the
// drift gate would fail-open after the upgrade and let through deploys it
// should have refused.
func TestReadSourceVersion_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(LegacySourceVersionPath(dir), []byte("legacy-id\n"), 0644); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	got, err := ReadSourceVersion(dir, testSidecarCID, testSidecarApp)
	if err != nil {
		t.Fatalf("ReadSourceVersion: %v", err)
	}
	if got != "legacy-id" {
		t.Errorf("got %q, want %q (legacy file must be read when per-app missing)", got, "legacy-id")
	}
}

// TestWriteSourceVersion_WritesPerAppOnly asserts the legacy path is
// never created by writes. The legacy file becomes a stale orphan after
// the next deploy/pull, which is harmless.
func TestWriteSourceVersion_WritesPerAppOnly(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "id-1"); err != nil {
		t.Fatalf("WriteSourceVersion: %v", err)
	}
	if _, err := os.Stat(SourceVersionPath(dir, testSidecarCID, testSidecarApp)); err != nil {
		t.Errorf("per-app file should exist: %v", err)
	}
	if _, err := os.Stat(LegacySourceVersionPath(dir)); !os.IsNotExist(err) {
		t.Errorf("legacy file must not be created by writes; stat err=%v", err)
	}
}

// TestSourceVersionFilename_PerApp asserts the leaf-name format. Two
// different apps in the same cluster must produce distinct filenames so
// they don't collide in a shared directory.
func TestSourceVersionFilename_PerApp(t *testing.T) {
	a := SourceVersionFilename("mycluster3", "appid4")
	b := SourceVersionFilename("mycluster3", "appid5")
	if a == b {
		t.Errorf("two apps in same cluster must produce distinct filenames: %q == %q", a, b)
	}
	if !strings.HasPrefix(a, ".runos.mycluster3.appid4.") || !strings.HasSuffix(a, ".source-version") {
		t.Errorf("unexpected filename shape: %q", a)
	}
}

// ---------------------------------------------------------------------------
// ComputeCodeVersionStatus
// ---------------------------------------------------------------------------

// archivesServer stands up a minimal httptest server that answers
// ListCliArchives with the conductor's envelope shape (manifest 9.0.0+)
// and falls back to {} for any other path.
func archivesServer(t *testing.T, archives []CliArchive) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cli-archives") {
			writeJSON(t, w, 200, cliArchivesEnvelope{
				CID:      testSidecarCID,
				AppID:    testSidecarApp,
				AppName:  "test-app",
				Archives: archives,
			})
			return
		}
		writeJSON(t, w, 200, map[string]any{})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestComputeCodeVersionStatus_NoSidecarReturnsNil(t *testing.T) {
	dir := t.TempDir()
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "v1", PushTime: "2026-04-27T10:00:00Z"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if status != nil {
		t.Errorf("expected nil for missing sidecar, got: %+v", status)
	}
}

func TestComputeCodeVersionStatus_UpToDate(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "v2"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "v1", PushTime: "2026-04-26T10:00:00Z"},
		{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if status == nil {
		t.Fatal("expected non-nil status")
	}
	if !status.HasBaseline() {
		t.Errorf("HasBaseline() = false; want true")
	}
	if !status.RecordedFound {
		t.Error("RecordedFound should be true when sidecar matches an archive")
	}
	if status.IsStale() {
		t.Errorf("IsStale() = true; expected false (recorded == latest)")
	}
	if status.NewerCount != 0 {
		t.Errorf("NewerCount = %d; want 0", status.NewerCount)
	}
	if status.Latest != "v2" {
		t.Errorf("Latest = %q; want v2", status.Latest)
	}
}

func TestComputeCodeVersionStatus_StaleListsNewerOldestFirst(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "v1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		// Order returned by server is unpredictable; the helper sorts.
		{CliUploadID: "v3", PushTime: "2026-04-28T10:00:00Z"},
		{CliUploadID: "v1", PushTime: "2026-04-26T10:00:00Z"},
		{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if !status.IsStale() {
		t.Fatal("expected IsStale() = true")
	}
	if status.NewerCount != 2 {
		t.Errorf("NewerCount = %d; want 2", status.NewerCount)
	}
	if len(status.NewerArchives) != 2 ||
		status.NewerArchives[0].CliUploadID != "v2" ||
		status.NewerArchives[1].CliUploadID != "v3" {
		t.Errorf("NewerArchives = %+v; want [v2, v3] (oldest-first)", status.NewerArchives)
	}
	if status.Latest != "v3" {
		t.Errorf("Latest = %q; want v3", status.Latest)
	}
}

func TestComputeCodeVersionStatus_RecordedNotInList(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "purged-id"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if !status.HasBaseline() {
		t.Errorf("HasBaseline() should be true (sidecar exists)")
	}
	if status.RecordedFound {
		t.Errorf("RecordedFound should be false; recorded id isn't in list")
	}
	if status.IsStale() {
		t.Errorf("IsStale() should be false when no anchor")
	}
	if status.NewerCount != 0 {
		t.Errorf("NewerCount should be 0 when no anchor; got %d", status.NewerCount)
	}
}

func TestComputeCodeVersionStatus_PropagatesListError(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "v1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	_, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err == nil {
		t.Fatal("expected error from failing ListCliArchives")
	}
	if !strings.Contains(err.Error(), "list archives") {
		t.Errorf("error should wrap as 'list archives'; got: %v", err)
	}
}

// TestComputeCodeVersionStatus_LegacyFallback covers a single-app project
// pulled before per-app sidecars existed: only the legacy file is on
// disk, but the gate must still find the anchor.
func TestComputeCodeVersionStatus_LegacyFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(LegacySourceVersionPath(dir), []byte("v1\n"), 0644); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "v1", PushTime: "2026-04-26T10:00:00Z"},
		{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if status == nil || !status.HasBaseline() {
		t.Fatal("expected baseline found via legacy fallback")
	}
	if !status.IsStale() || status.NewerCount != 1 {
		t.Errorf("expected stale with NewerCount=1; got %+v", status)
	}
}

// FPL16 B2, MEASURED on the dev homelab cluster before this fix.
//
// A build that fails still registers its uploaded source archive on the server, and that listing
// is the only signal this gate has. The sequence was: deploy succeeds (archive A), a bad line is
// added to the Dockerfile, the deploy's build FAILS (archive B), the bad line is removed so the
// tree is byte-identical to A, and the next deploy is REFUSED with "newer source archives exist on
// the server than your recorded baseline. Deploying now would overwrite changes that aren't in
// your local files."
//
// Both sentences are false: B IS the user's own files, uploaded by the failed build. The printed
// recovery, `apps pull --code --force`, would have overwritten the working tree with the source of
// a build that failed.
//
// Conductor cannot fix this alone, because it cannot know which id the caller's sidecar holds. It
// annotates every archive with buildStatus and leaves the decision here.
func TestComputeCodeVersionStatus_FailedBuildIsNotNewer(t *testing.T) {
	dir := t.TempDir()
	// The --follow path: the sidecar was restored to the last GOOD upload.
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "A"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "A", PushTime: "2026-04-26T10:00:00Z", BuildStatus: "success"},
		{CliUploadID: "B", PushTime: "2026-04-27T10:00:00Z", BuildStatus: "failed"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if !status.RecordedFound {
		t.Fatal("RecordedFound = false; the recorded archive must still resolve")
	}
	if status.NewerCount != 0 {
		t.Errorf("NewerCount = %d; want 0. An upload whose build failed never shipped an image, so it is not a deploy this directory is behind", status.NewerCount)
	}
	if status.IsStale() {
		t.Error("IsStale() = true; the deploy would be refused for a build that never shipped")
	}
}

// The DEFAULT deploy path, which is the larger half of the traffic. `runos deploy` writes the
// sidecar to the NEW upload id unconditionally and only restores the previous value inside
// `if flagFollow`, and --follow defaults to false. So after a default deploy whose build failed,
// the sidecar names the FAILED upload.
//
// This is why conductor must not DROP the failed archive from the listing: dropping it makes the
// recorded id unresolvable, and the gate then hard-refuses with "the local .source-version file
// was hand-edited or the archive was deleted server-side", which is a harder refusal than the
// warning it replaced, on a path that worked before.
func TestComputeCodeVersionStatus_FailedBuildStillResolves(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "B"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "A", PushTime: "2026-04-26T10:00:00Z", BuildStatus: "success"},
		{CliUploadID: "B", PushTime: "2026-04-27T10:00:00Z", BuildStatus: "failed"},
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if !status.RecordedFound {
		t.Fatal("RecordedFound = false; a failed build's archive must stay resolvable or the next deploy hits the tampering refusal")
	}
	if status.NewerCount != 0 {
		t.Errorf("NewerCount = %d; want 0", status.NewerCount)
	}
}

// Absent is unknown, never failed. Build rows are capped and purged, so an old archive routinely
// carries no status. Reading absent as failed would silently stop protecting a teammate's deploy,
// which is the whole reason this gate exists.
func TestComputeCodeVersionStatus_UnknownBuildStatusStillCounts(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSourceVersion(dir, testSidecarCID, testSidecarApp, "A"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	srv := archivesServer(t, []CliArchive{
		{CliUploadID: "A", PushTime: "2026-04-26T10:00:00Z", BuildStatus: "success"},
		{CliUploadID: "C", PushTime: "2026-04-28T10:00:00Z"}, // purged row: no status at all
	})
	svc := NewService(srv.URL, "tok", testSidecarCID, "acc-1")

	status, err := ComputeCodeVersionStatus(svc, testSidecarCID, testSidecarApp, dir)
	if err != nil {
		t.Fatalf("ComputeCodeVersionStatus: %v", err)
	}
	if status.NewerCount != 1 {
		t.Errorf("NewerCount = %d; want 1. An archive with no build row must still protect a teammate's deploy", status.NewerCount)
	}
}
