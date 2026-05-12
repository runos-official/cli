package apps

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// ComputeYAMLDiff
// ---------------------------------------------------------------------------

func TestComputeYAMLDiff_InSyncWhenBytesMatch(t *testing.T) {
	dir := t.TempDir()
	p := &PulledApp{
		App:        "svc",
		DeployType: "cli",
		ID:         "x",
		CID:        "k1",
		AID:        "acc-1",
		Replicas:   1,
		ServicePortMappings: []Port{{Port: 80, StandardHttps: true}},
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "svc.yaml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeYAMLDiff(path, p)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync (diff:\n%s)", got.Status, got.UnifiedDiff)
	}
	if got.UnifiedDiff != "" {
		t.Errorf("UnifiedDiff should be empty on in_sync, got:\n%s", got.UnifiedDiff)
	}
}

func TestComputeYAMLDiff_DriftProducesUnifiedDiff(t *testing.T) {
	dir := t.TempDir()
	local := &PulledApp{
		App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1",
		Replicas: 1,
		ServicePortMappings: []Port{{Port: 80, StandardHttps: true}},
	}
	localBytes, _ := yaml.Marshal(local)
	path := filepath.Join(dir, "svc.yaml")
	if err := os.WriteFile(path, localBytes, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	server := *local
	server.Replicas = 3

	got, err := ComputeYAMLDiff(path, &server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("status = %q, want drift", got.Status)
	}
	if !strings.Contains(got.UnifiedDiff, "-replicas: 1") || !strings.Contains(got.UnifiedDiff, "+replicas: 3") {
		t.Errorf("unexpected unified diff:\n%s", got.UnifiedDiff)
	}
	// Format sanity: should look like a unified diff.
	if !strings.Contains(got.UnifiedDiff, "--- local") || !strings.Contains(got.UnifiedDiff, "+++ server") {
		t.Errorf("diff missing unified-diff headers:\n%s", got.UnifiedDiff)
	}
}

func TestComputeYAMLDiff_LocalMissing(t *testing.T) {
	dir := t.TempDir()
	server := &PulledApp{App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 80, StandardHttps: true}}}
	path := filepath.Join(dir, "missing.yaml")

	got, err := ComputeYAMLDiff(path, server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusLocalMissing {
		t.Errorf("status = %q, want local_missing", got.Status)
	}
	if got.Path != path {
		t.Errorf("path = %q, want %q", got.Path, path)
	}
}

// ---------------------------------------------------------------------------
// ComputeEnvDiff
// ---------------------------------------------------------------------------

func TestComputeEnvDiff_InSync(t *testing.T) {
	dir := t.TempDir()
	vars := map[string]string{"A": "1", "B": "2"}

	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, RenderEnvBytes(vars), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeEnvDiff(path, vars)
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync", got.Status)
	}
}

func TestComputeEnvDiff_DriftShowsChangedVar(t *testing.T) {
	dir := t.TempDir()
	localVars := map[string]string{"A": "1", "B": "2"}
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, RenderEnvBytes(localVars), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	serverVars := map[string]string{"A": "1", "B": "NEW", "C": "3"}

	got, err := ComputeEnvDiff(path, serverVars)
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("status = %q, want drift", got.Status)
	}
	if !strings.Contains(got.UnifiedDiff, "-B=2") || !strings.Contains(got.UnifiedDiff, "+B=NEW") {
		t.Errorf("diff missing changed B:\n%s", got.UnifiedDiff)
	}
	if !strings.Contains(got.UnifiedDiff, "+C=3") {
		t.Errorf("diff missing added C:\n%s", got.UnifiedDiff)
	}
}

func TestComputeEnvDiff_LocalMissing(t *testing.T) {
	got, err := ComputeEnvDiff(filepath.Join(t.TempDir(), ".env"), map[string]string{"A": "1"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusLocalMissing {
		t.Errorf("status = %q, want local_missing", got.Status)
	}
}

// Regression test for I2-1c (TEST_LOG.md): a freshly-pulled
// `runos.<cid>.<id>.config.env` file with the auto-written 3-line comment
// header but zero env vars must not show as drift against a server that
// has zero env vars. Without this, every clean deploy trips the drift gate
// and CI `apps diff --json` exits 2.
func TestComputeEnvDiff_CommentOnlyLocal_InSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runos.mycluster2.appid2.config.env")
	header := []byte("# Plain ConfigMap-backed env vars for this app on cluster mycluster2.\n" +
		"# Committed to VCS, never put credentials here (use the .runos.<cid>.<id>.env\n" +
		"# secret file instead, which is gitignored). Lines are KEY=value, # comments allowed.\n")
	if err := os.WriteFile(path, header, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeEnvDiff(path, map[string]string{})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync (comment-only local should not be drift):\n%s", got.Status, got.UnifiedDiff)
	}
}

// I2-1c partner: same key-value pairs but one side has an extra comment
// line / different ordering must still report in_sync.
func TestComputeEnvDiff_SameVarsDifferentShape_InSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	// Local has comments and inverted ordering.
	body := []byte("# header line\n\nB=2\n# inline\nA=1\n")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeEnvDiff(path, map[string]string{"A": "1", "B": "2"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync", got.Status)
	}
}

// I2-1c partner: an actual KV delta must still surface as drift even when
// comments are present.
func TestComputeEnvDiff_RealDeltaWithComments_StillDrift(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	body := []byte("# header\nA=local\n")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeEnvDiff(path, map[string]string{"A": "server"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("status = %q, want drift", got.Status)
	}
}

// ---------------------------------------------------------------------------
// ComputeSecretFilesDiff
// ---------------------------------------------------------------------------

func TestComputeSecretFilesDiff_MixedStatuses(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, SecretFilesDirname())
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// One matching file.
	matchingContent := []byte("hello\n")
	matchingMd5 := md5Hex(matchingContent)
	if err := os.WriteFile(filepath.Join(secretsDir, "match.txt"), matchingContent, 0600); err != nil {
		t.Fatalf("write match: %v", err)
	}

	// One file whose local bytes differ from the server md5.
	if err := os.WriteFile(filepath.Join(secretsDir, "drift.txt"), []byte("local version"), 0600); err != nil {
		t.Fatalf("write drift: %v", err)
	}

	// One file present on server but missing locally, no write here.

	serverFiles := []SecretFileSummary{
		{Filename: "match.txt", MountPath: "/a", MD5: matchingMd5},
		{Filename: "drift.txt", MountPath: "/b", MD5: "server-md5-that-wont-match"},
		{Filename: "missing.txt", MountPath: "/c", MD5: "whatever"},
	}

	paths := map[string]string{
		"match.txt": filepath.Join(secretsDir, "match.txt"),
		"drift.txt": filepath.Join(secretsDir, "drift.txt"),
		// missing.txt intentionally absent from the map → local_missing
	}
	got, err := ComputeSecretFilesDiff(paths, serverFiles)
	if err != nil {
		t.Fatalf("ComputeSecretFilesDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("aggregate status = %q, want drift", got.Status)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got.Entries))
	}

	byName := map[string]SecretFileDiff{}
	for _, e := range got.Entries {
		byName[e.Filename] = e
	}
	if byName["match.txt"].Status != StatusInSync {
		t.Errorf("match.txt: status = %q, want in_sync", byName["match.txt"].Status)
	}
	if byName["drift.txt"].Status != StatusDrift {
		t.Errorf("drift.txt: status = %q, want drift", byName["drift.txt"].Status)
	}
	if byName["drift.txt"].ServerMd5 != "server-md5-that-wont-match" {
		t.Errorf("drift.txt: ServerMd5 = %q", byName["drift.txt"].ServerMd5)
	}
	if byName["drift.txt"].LocalMd5 == "" {
		t.Errorf("drift.txt: LocalMd5 should be computed")
	}
	if byName["missing.txt"].Status != StatusLocalMissing {
		t.Errorf("missing.txt: status = %q, want local_missing", byName["missing.txt"].Status)
	}
}

func TestComputeSecretFilesDiff_AllInSync(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, SecretFilesDirname())
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := []byte("match")
	if err := os.WriteFile(filepath.Join(secretsDir, "a"), content, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	paths := map[string]string{"a": filepath.Join(secretsDir, "a")}
	got, err := ComputeSecretFilesDiff(paths, []SecretFileSummary{
		{Filename: "a", MD5: md5Hex(content)},
	})
	if err != nil {
		t.Fatalf("ComputeSecretFilesDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("aggregate status = %q, want in_sync", got.Status)
	}
	if len(got.Entries) != 1 || got.Entries[0].Status != StatusInSync {
		t.Errorf("entries: %+v", got.Entries)
	}
}

func TestEnrichSecretFileDiffsWithContent(t *testing.T) {
	// Stand up a fake server that returns base64-encoded content for any
	// secret file fetch. local md5 mismatches drive the drift status; the
	// helper should fetch + write a unified diff body.
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, SecretFilesDirname())
	if err := os.MkdirAll(secretsDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "drifted"), []byte("local-version\n"), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "matching"), []byte("same\n"), 0600); err != nil {
		t.Fatalf("write matching: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /acc-1/k1/apps/ab12c/secret-files/<filename>
		filename := filepath.Base(r.URL.Path)
		var body string
		switch filename {
		case "drifted":
			body = "server-version\n"
		case "missing-locally":
			body = "server-only\n"
		default:
			t.Errorf("unexpected fetch for %q", filename)
		}
		writeJSON(t, w, 200, SecretFileContent{
			Filename:  filename,
			MountPath: "/etc/secrets/" + filename,
			MD5:       md5Hex([]byte(body)),
			Content:   base64.StdEncoding.EncodeToString([]byte(body)),
		})
	}))
	defer srv.Close()

	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	files := SecretFilesDiff{
		Status: StatusDrift,
		Entries: []SecretFileDiff{
			{Filename: "drifted", Status: StatusDrift, ServerMd5: "x", LocalMd5: "y", Local: filepath.Join(secretsDir, "drifted")},
			{Filename: "matching", Status: StatusInSync, ServerMd5: "z", LocalMd5: "z", Local: filepath.Join(secretsDir, "matching")},
			{Filename: "missing-locally", Status: StatusLocalMissing, ServerMd5: "w"},
		},
	}

	if err := EnrichSecretFileDiffsWithContent(svc, "ab12c", &files); err != nil {
		t.Fatalf("EnrichSecretFileDiffsWithContent: %v", err)
	}

	// In-sync entry should not have been enriched (no unnecessary fetch).
	if files.Entries[1].UnifiedDiff != "" {
		t.Errorf("in-sync entry should not get enriched, got UnifiedDiff:\n%s", files.Entries[1].UnifiedDiff)
	}
	// Drifted entry should have a unified diff with both the local and
	// server content visible.
	driftBody := files.Entries[0].UnifiedDiff
	if !strings.Contains(driftBody, "-local-version") || !strings.Contains(driftBody, "+server-version") {
		t.Errorf("drifted unified diff missing expected lines:\n%s", driftBody)
	}
	// Local-missing entry should diff empty-string against server content,
	// producing a body that adds the server-only content.
	missingBody := files.Entries[2].UnifiedDiff
	if !strings.Contains(missingBody, "+server-only") {
		t.Errorf("missing-locally diff should show server addition:\n%s", missingBody)
	}
}

func TestComputeSecretFilesDiff_EmptyWhenNoServerFiles(t *testing.T) {
	got, err := ComputeSecretFilesDiff(map[string]string{}, nil)
	if err != nil {
		t.Fatalf("ComputeSecretFilesDiff: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync for empty input", got.Status)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries should be empty, got %+v", got.Entries)
	}
}

// ---------------------------------------------------------------------------
// ComputeOverridesDiff
// ---------------------------------------------------------------------------

func TestComputeOverridesDiff_MixedStatuses(t *testing.T) {
	dir := t.TempDir()
	overridesDir := filepath.Join(dir, OverridesDirname())
	if err := os.MkdirAll(overridesDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	matching := []byte("spec:\n  replicas: 2\n")
	drifted := []byte("server-side content")

	// One file whose local bytes match the server base64-decoded bytes.
	if err := os.WriteFile(filepath.Join(overridesDir, "match.yaml"), matching, 0644); err != nil {
		t.Fatalf("write match: %v", err)
	}
	// One file whose local bytes differ from server bytes.
	if err := os.WriteFile(filepath.Join(overridesDir, "drift.yaml"), []byte("local content"), 0644); err != nil {
		t.Fatalf("write drift: %v", err)
	}

	serverOverrides := []OverrideSummary{
		{ID: "idA", Name: "match", Data: base64Encode(matching)},
		{ID: "idB", Name: "drift", Data: base64Encode(drifted)},
		{ID: "idC", Name: "missing", Data: base64Encode([]byte("anything"))},
	}

	paths := map[string]string{
		"idA": filepath.Join(overridesDir, "match.yaml"),
		"idB": filepath.Join(overridesDir, "drift.yaml"),
		// idC intentionally absent → local_missing
	}
	got, err := ComputeOverridesDiff(paths, serverOverrides)
	if err != nil {
		t.Fatalf("ComputeOverridesDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("aggregate status = %q, want drift", got.Status)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got.Entries))
	}

	byID := map[string]OverrideDiff{}
	for _, e := range got.Entries {
		byID[e.ID] = e
	}
	if byID["idA"].Status != StatusInSync {
		t.Errorf("idA: status = %q, want in_sync", byID["idA"].Status)
	}
	if byID["idB"].Status != StatusDrift {
		t.Errorf("idB: status = %q, want drift", byID["idB"].Status)
	}
	if byID["idB"].ServerMd5 == "" || byID["idB"].LocalMd5 == "" {
		t.Errorf("idB: md5s should be populated on drift")
	}
	if byID["idC"].Status != StatusLocalMissing {
		t.Errorf("idC: status = %q, want local_missing", byID["idC"].Status)
	}
}

func TestComputeOverridesDiff_RejectsMalformedBase64(t *testing.T) {
	_, err := ComputeOverridesDiff(map[string]string{}, []OverrideSummary{
		{ID: "bad", Name: "broken", Data: "!!!not-base64!!!"},
	})
	if err == nil {
		t.Fatal("expected error on invalid base64")
	}
}

// ---------------------------------------------------------------------------
// BuildServerStateForDiff
// ---------------------------------------------------------------------------

func TestBuildServerStateForDiff_MatchesPullShape(t *testing.T) {
	raw := map[string]any{
		"id": "ab12c", "name": "svc", "replicas": float64(1),
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"integrationType":            nil,
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}
	envs := map[string]string{"A": "1"}
	secretFiles := []SecretFileSummary{
		{Filename: "server.crt", MountPath: "/etc/ssl/server.crt", MD5: "abc"},
	}

	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, envs, secretFiles, nil, nil)

	if p.Env != "runos.k1.ab12c.config.env" {
		t.Errorf("Env = %q, want runos.k1.ab12c.config.env (envs present)", p.Env)
	}
	if len(p.SecretFiles) != 1 {
		t.Fatalf("SecretFiles len = %d, want 1", len(p.SecretFiles))
	}
	sf := p.SecretFiles[0]
	if sf.Filename != "server.crt" || sf.MountPath != "/etc/ssl/server.crt" || sf.MD5 != "abc" {
		t.Errorf("SecretFile metadata wrong: %+v", sf)
	}
	if sf.Local != filepath.Join(".secret-files", "server.crt") {
		t.Errorf("SecretFile.Local = %q", sf.Local)
	}
}

// TestBuildServerStateForDiff_RequiresFromMap asserts that the requires
// map handed in (matching /requires output) is copied verbatim
// into the PulledApp, including server-authoritative Config and Env.
// Class is intentionally absent from the server response.
func TestBuildServerStateForDiff_RequiresFromMap(t *testing.T) {
	raw := map[string]any{"id": "ab12c", "name": "web", "replicas": float64(1)}
	requires := map[string]ServiceRequirement{
		"poll-app-db": {
			Type:   "postgresql",
			ID:     "mjn1d",
			Config: map[string]any{"databaseName": "pollapp"},
			Env:    map[string]string{"url": "DATABASE_URL"},
		},
		"poll-app-cache": {Type: "valkey", ID: "xY9zW"},
	}
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, requires)
	if len(p.Requires) != 2 {
		t.Fatalf("expected 2 requires entries, got %d: %+v", len(p.Requires), p.Requires)
	}
	db := p.Requires["poll-app-db"]
	if db.Type != "postgresql" || db.ID != "mjn1d" {
		t.Errorf("poll-app-db type/id: got %+v", db)
	}
	if db.Config["databaseName"] != "pollapp" {
		t.Errorf("config should be passed through verbatim; got %+v", db.Config)
	}
	if db.Env["url"] != "DATABASE_URL" {
		t.Errorf("env should be passed through verbatim; got %+v", db.Env)
	}
	if db.Class != "" {
		t.Errorf("class must stay empty (server doesn't expose it); got %q", db.Class)
	}
}

// TestBuildServerStateForDiff_DefensiveCopy asserts that mutating the
// caller's input map after the call doesn't bleed into the returned
// PulledApp.
func TestBuildServerStateForDiff_DefensiveCopy(t *testing.T) {
	raw := map[string]any{"id": "ab12c", "name": "web", "replicas": float64(1)}
	requires := map[string]ServiceRequirement{
		"poll-app-db": {Type: "postgresql", ID: "mjn1d"},
	}
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, requires)
	delete(requires, "poll-app-db")
	if _, ok := p.Requires["poll-app-db"]; !ok {
		t.Error("PulledApp.Requires should not alias caller's map")
	}
}

// TestBuildServerStateForDiff_NilRequiresLeavesRequiresNil pins the
// "didn't fetch requires" signal: nil input map produces nil Requires
// map, which omitempty drops from the marshaled yaml.
func TestBuildServerStateForDiff_NilRequiresLeavesRequiresNil(t *testing.T) {
	raw := map[string]any{"id": "x", "name": "svc", "replicas": float64(1)}
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	if p.Requires != nil {
		t.Errorf("nil deps must leave Requires nil; got %+v", p.Requires)
	}
}

func TestBuildServerStateForDiff_OmitsEnvWhenEmpty(t *testing.T) {
	raw := map[string]any{"id": "x", "name": "svc", "replicas": float64(1)}
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, map[string]string{}, nil, nil, nil)
	if p.Env != "" {
		t.Errorf("Env should be empty when no envs, got %q", p.Env)
	}
	if p.SecretFiles != nil {
		t.Errorf("SecretFiles should be nil when empty, got %+v", p.SecretFiles)
	}
}

// ---------------------------------------------------------------------------
// AdditiveOnly classification + NeedsForceToOverwrite gating
// ---------------------------------------------------------------------------

func TestComputeYAMLDiff_AdditiveOnlyWhenServerHasMore(t *testing.T) {
	dir := t.TempDir()
	local := &PulledApp{
		App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1",
		Replicas: 1,
		ServicePortMappings: []Port{{Port: 80, StandardHttps: true}},
	}
	localBytes, _ := yaml.Marshal(local)
	path := filepath.Join(dir, "svc.yaml")
	if err := os.WriteFile(path, localBytes, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	server := *local
	server.SecretFiles = []SecretFile{{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m"}}

	got, err := ComputeYAMLDiff(path, &server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Fatalf("status = %q, want drift", got.Status)
	}
	if !got.AdditiveOnly {
		t.Errorf("AdditiveOnly should be true when server only adds:\n%s", got.UnifiedDiff)
	}
}

// TestComputeYAMLDiff_LocalIsSupersetOnNestedArrayAddition pins I10-R
// direct-deploy: when the local yaml appends a new entry to a nested
// array (e.g. `secretFiles[]`) and the prior entries are unchanged,
// the diff classifier reports `LocalIsSuperset: true` so the deploy
// drift gate (`NeedsForceToDeploy`) waves the deploy through without
// requiring --force. Pre-fix the array branch in `valuesAdditive`
// required strict length equality, treating "user added a new entry"
// as a generic length mismatch and tripping the gate.
func TestComputeYAMLDiff_LocalIsSupersetOnNestedArrayAddition(t *testing.T) {
	dir := t.TempDir()
	server := &PulledApp{
		App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1",
		Replicas:            1,
		ServicePortMappings: []Port{{Port: 80, StandardHttps: true}},
		SecretFiles:         []SecretFile{{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m1"}},
	}

	local := *server
	local.SecretFiles = []SecretFile{
		{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m1"},
		{Filename: "b", MountPath: "/b", Local: ".s/b", MD5: "m2"}, // appended entry
	}
	localBytes, _ := yaml.Marshal(&local)
	path := filepath.Join(dir, "svc.yaml")
	if err := os.WriteFile(path, localBytes, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ComputeYAMLDiff(path, server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Fatalf("status = %q, want drift", got.Status)
	}
	if !got.LocalIsSuperset {
		t.Errorf("LocalIsSuperset should be true when local appends to a nested array (I10-R):\n%s", got.UnifiedDiff)
	}

	// And the deploy gate should NOT need --force for this case.
	report := &DiffReport{YAML: got}
	if report.NeedsForceToDeploy() {
		t.Errorf("NeedsForceToDeploy should be false on pure local-additions to nested array")
	}
}

// TestValuesAdditive_ReorderedArrayStillTripsGate ensures the I10-R
// prefix-match relaxation doesn't silently waive REAL drift: arrays
// where local reorders or replaces an entry (vs simply appending) are
// still classified as drift, not additive.
func TestValuesAdditive_ReorderedArrayStillTripsGate(t *testing.T) {
	server := &PulledApp{
		App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1",
		Replicas:            1,
		ServicePortMappings: []Port{{Port: 80, StandardHttps: true}},
		SecretFiles: []SecretFile{
			{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m1"},
			{Filename: "b", MountPath: "/b", Local: ".s/b", MD5: "m2"},
		},
	}

	dir := t.TempDir()

	// Case A: reorder (swap entries). Length matches but positions differ.
	localReorder := *server
	localReorder.SecretFiles = []SecretFile{
		{Filename: "b", MountPath: "/b", Local: ".s/b", MD5: "m2"},
		{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m1"},
	}
	pathA := filepath.Join(dir, "reorder.yaml")
	rb, _ := yaml.Marshal(&localReorder)
	_ = os.WriteFile(pathA, rb, 0644)

	diffA, err := ComputeYAMLDiff(pathA, server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff A: %v", err)
	}
	if diffA.LocalIsSuperset {
		t.Errorf("reorder should NOT be LocalIsSuperset")
	}
	if diffA.AdditiveOnly {
		t.Errorf("reorder should NOT be AdditiveOnly")
	}

	// Case B: middle-insert (3 local entries, second is new).
	localInsert := *server
	localInsert.SecretFiles = []SecretFile{
		{Filename: "a", MountPath: "/a", Local: ".s/a", MD5: "m1"},
		{Filename: "x", MountPath: "/x", Local: ".s/x", MD5: "mX"}, // inserted in the middle
		{Filename: "b", MountPath: "/b", Local: ".s/b", MD5: "m2"},
	}
	pathB := filepath.Join(dir, "insert.yaml")
	ib, _ := yaml.Marshal(&localInsert)
	_ = os.WriteFile(pathB, ib, 0644)

	diffB, err := ComputeYAMLDiff(pathB, server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff B: %v", err)
	}
	if diffB.LocalIsSuperset {
		t.Errorf("middle-insert should NOT be LocalIsSuperset (position-stable expectation)")
	}
}

func TestComputeYAMLDiff_NotAdditiveWhenLocalDiverges(t *testing.T) {
	dir := t.TempDir()
	local := &PulledApp{App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 80, StandardHttps: true}}}
	localBytes, _ := yaml.Marshal(local)
	path := filepath.Join(dir, "svc.yaml")
	_ = os.WriteFile(path, localBytes, 0644)

	server := *local
	server.Replicas = 3 // local says 1, server says 3, divergent

	got, err := ComputeYAMLDiff(path, &server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift || got.AdditiveOnly {
		t.Errorf("AdditiveOnly should be false on divergent value: %+v", got)
	}
}

func TestComputeYAMLDiff_NotAdditiveWhenLocalHasExtraField(t *testing.T) {
	// Build a yaml file with a key the server's PulledApp doesn't have,
	// to exercise mapIsSubset's "local has extra key" path.
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.yaml")
	_ = os.WriteFile(path, []byte(`app: svc
deployType: cli
id: x
cid: k1
aid: acc-1
replicas: 1
extraField: should-not-exist-on-server
servicePortMappings:
    - port: 80
      standardHttps: true
`), 0644)

	server := &PulledApp{App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 80, StandardHttps: true}}}

	got, err := ComputeYAMLDiff(path, server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift || got.AdditiveOnly {
		t.Errorf("AdditiveOnly should be false when local has extra field: %+v", got)
	}
}

func TestComputeEnvDiff_AdditiveOnlyWhenServerHasMore(t *testing.T) {
	dir := t.TempDir()
	localVars := map[string]string{"A": "1", "B": "2"}
	if err := os.WriteFile(filepath.Join(dir, ".env"), RenderEnvBytes(localVars), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ComputeEnvDiff(filepath.Join(dir, ".env"), map[string]string{"A": "1", "B": "2", "C": "3"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift || !got.AdditiveOnly {
		t.Errorf("expected drift+additive, got %+v", got)
	}
	// I8-E: localIsSuperset is the dual of additiveOnly. When server has
	// strict-superset of local (additive on the server side), local is
	// NOT a superset.
	if got.LocalIsSuperset {
		t.Errorf("server has strict-superset of local; localIsSuperset should be false, got %+v", got)
	}
}

// TestComputeEnvDiff_LocalIsSupersetWhenLocalHasMore pins the
// previously-missing assignment that left LocalIsSuperset false even
// when local was a strict superset of server. Regression target: I8-E.
func TestComputeEnvDiff_LocalIsSupersetWhenLocalHasMore(t *testing.T) {
	dir := t.TempDir()
	// Local has all 3 server keys plus EXTRA1 and EXTRA2.
	localVars := map[string]string{
		"FEATURE_FAVICONS": "true",
		"LOG_LEVEL":        "debug",
		"MCP_HOT_UPDATE":   "yes",
		"EXTRA1":           "val1",
		"EXTRA2":           "val2",
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), RenderEnvBytes(localVars), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	serverVars := map[string]string{
		"FEATURE_FAVICONS": "true",
		"LOG_LEVEL":        "debug",
		"MCP_HOT_UPDATE":   "yes",
	}
	got, err := ComputeEnvDiff(filepath.Join(dir, ".env"), serverVars)
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Fatalf("expected drift, got %s", got.Status)
	}
	if got.AdditiveOnly {
		t.Errorf("local has extras over server; additiveOnly should be false, got %+v", got)
	}
	if !got.LocalIsSuperset {
		t.Errorf("local is strict superset of server; localIsSuperset should be true, got %+v", got)
	}
}

// TestComputeEnvDiffFiltered_LocalIsSuperset pins the same fix on the
// platform-injected-filter codepath (BuildDiffReport uses this when
// requires:<alias>.env claims a name). Regression target: I8-E.
func TestComputeEnvDiffFiltered_LocalIsSuperset(t *testing.T) {
	dir := t.TempDir()
	// Local has every server key plus an extra user key. Platform-injected
	// names should be stripped from both sides before comparison.
	localVars := map[string]string{
		"USER_KEY":     "u",
		"EXTRA":        "x",
		"DATABASE_URL": "local-shadow-ignored",
	}
	serverVars := map[string]string{
		"USER_KEY":     "u",
		"DATABASE_URL": "platform-injected",
		"REDIS_HOST":   "platform-injected",
	}
	platformInjected := map[string]bool{"DATABASE_URL": true, "REDIS_HOST": true}
	if err := os.WriteFile(filepath.Join(dir, ".env"), RenderEnvBytes(localVars), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ComputeEnvDiffFiltered(filepath.Join(dir, ".env"), serverVars, platformInjected)
	if err != nil {
		t.Fatalf("ComputeEnvDiffFiltered: %v", err)
	}
	if got.Status != StatusDrift {
		t.Fatalf("expected drift, got %s", got.Status)
	}
	if got.AdditiveOnly {
		t.Errorf("local has EXTRA beyond server's filtered view; additiveOnly should be false: %+v", got)
	}
	if !got.LocalIsSuperset {
		t.Errorf("after stripping platform-injected, local is strict superset; localIsSuperset should be true: %+v", got)
	}
}

func TestComputeEnvDiff_NotAdditiveWhenLocalHasExtraOrDifferingKey(t *testing.T) {
	dir := t.TempDir()

	// Local has a key server doesn't.
	if err := os.WriteFile(filepath.Join(dir, "extra.env"), RenderEnvBytes(map[string]string{"A": "1", "EXTRA": "x"}), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ComputeEnvDiff(filepath.Join(dir, "extra.env"), map[string]string{"A": "1"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift || got.AdditiveOnly {
		t.Errorf("local-only key should NOT be additive, got %+v", got)
	}

	// Local has a different value for a shared key.
	if err := os.WriteFile(filepath.Join(dir, "diff.env"), RenderEnvBytes(map[string]string{"A": "old"}), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = ComputeEnvDiff(filepath.Join(dir, "diff.env"), map[string]string{"A": "new"})
	if err != nil {
		t.Fatalf("ComputeEnvDiff: %v", err)
	}
	if got.Status != StatusDrift || got.AdditiveOnly {
		t.Errorf("divergent value should NOT be additive, got %+v", got)
	}
}

func TestNeedsForceToOverwrite_AdditiveDriftDoesntForce(t *testing.T) {
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusDrift, AdditiveOnly: true},
		Env:         SectionDiff{Status: StatusDrift, AdditiveOnly: true},
		SecretFiles: SecretFilesDiff{Status: StatusDrift, Entries: []SecretFileDiff{{Filename: "a", Status: StatusLocalMissing}}},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if r.NeedsForceToOverwrite() {
		t.Error("force should NOT be required when all drift is additive / local_missing")
	}
}

func TestNeedsForceToOverwrite_DivergentYAMLForces(t *testing.T) {
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusDrift, AdditiveOnly: false},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if !r.NeedsForceToOverwrite() {
		t.Error("force should be required when yaml diverges (not just additive)")
	}
}

// ---------------------------------------------------------------------------
// NeedsForceToDeploy, narrower than NeedsForceToOverwrite
// ---------------------------------------------------------------------------

func TestNeedsForceToDeploy_LocalSupersetIsBenign(t *testing.T) {
	// User added new fields locally (e.g. metricsPort, metricsPath);
	// server doesn't have them yet. Deploy will SET them on the
	// server. No clearing, no overwrite. The gate must let this
	// through without --force; it's the normal "I edited my yaml,
	// push it" flow.
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusDrift, LocalIsSuperset: true},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if r.NeedsForceToDeploy() {
		t.Error("local-superset yaml drift should not block deploy (user-intended push)")
	}
}

func TestNeedsForceToDeploy_ServerHasClearOnOmitFieldsBlocks(t *testing.T) {
	// Server has a clearOnOmit field local doesn't. Pushing would
	// wipe it (`domain:`, `healthCheck*`, etc. all silently clear on
	// omit), so the gate refuses without `--force` so the user takes
	// an explicit step.
	r := &DiffReport{
		YAML: SectionDiff{
			Status:           StatusDrift,
			AdditiveOnly:     true,
			ServerOnlyFields: []string{`domain ("foo.com")`},
		},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("server-only clearOnOmit fields must block deploy without --force")
	}
}

func TestNeedsForceToDeploy_ServerHasNonZeroPreserveOnOmitBlocks(t *testing.T) {
	// Server has a preserve-on-omit field with a non-zero value
	// local doesn't have. Pushing wouldn't clear it server-side, but
	// the user might have customised it server-side and expects it
	// to round-trip via the local yaml. Refuse so the user pulls
	// first and inspects.
	r := &DiffReport{
		YAML: SectionDiff{
			Status:           StatusDrift,
			AdditiveOnly:     true,
			ServerOnlyFields: []string{"cpuRequestMc (500)"},
		},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("server-only preserve-on-omit with non-zero value must block deploy without --force")
	}
}

// I3-E retest follow-up: the gate must NOT refuse when the only
// server-only delta is preserve-on-omit fields with zero-default
// summaries (cpuRequestMc=0, memoryRequestMb=0, etc.). Pre-fix, every
// "second deploy" of a freshly-provisioned app tripped the gate
// because the server emits these zero defaults that the local yaml
// hadn't pulled. Pushing leaves server state unchanged, so refusing
// was forcing a cosmetic `apps pull --force` for nothing.
func TestNeedsForceToDeploy_BenignZeroPreserveOnOmitPasses(t *testing.T) {
	r := &DiffReport{
		YAML: SectionDiff{
			Status:           StatusDrift,
			AdditiveOnly:     true,
			ServerOnlyFields: []string{"cpuRequestMc (0)", "memoryRequestMb (0)"},
		},
	}
	if r.NeedsForceToDeploy() {
		t.Error("benign zero-default preserve-on-omit server-only fields must NOT block deploy")
	}
}

// I3-E retest follow-up: a benign zero in the same list as a
// clearOnOmit (or non-zero preserve-on-omit) field must still block.
// One blocking entry is enough; the gate is conservative on mixed
// lists.
func TestNeedsForceToDeploy_MixedBenignAndBlockingStillBlocks(t *testing.T) {
	r := &DiffReport{
		YAML: SectionDiff{
			Status:           StatusDrift,
			AdditiveOnly:     true,
			ServerOnlyFields: []string{"cpuRequestMc (0)", `domain ("foo.com")`},
		},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("any clearOnOmit entry alongside benign zeros must still block")
	}
}

// I3-E retest follow-up: divergent shared values (AdditiveOnly=false)
// must always block, regardless of whether server-only entries are
// benign. The benign waiver only kicks in when local is otherwise a
// strict subset of server.
func TestNeedsForceToDeploy_DivergentValuesBlockEvenWithBenignServerOnly(t *testing.T) {
	r := &DiffReport{
		YAML: SectionDiff{
			Status:           StatusDrift,
			AdditiveOnly:     false, // shared field has divergent value
			ServerOnlyFields: []string{"cpuRequestMc (0)"},
		},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("divergent shared values must block even when only server-only entries are benign")
	}
}

func TestNeedsForceToDeploy_DivergentYamlBlocks(t *testing.T) {
	r := &DiffReport{
		YAML: SectionDiff{Status: StatusDrift},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("divergent yaml drift must block deploy")
	}
}

func TestNeedsForceToDeploy_StaleCodeBlocks(t *testing.T) {
	r := &DiffReport{
		YAML: SectionDiff{Status: StatusInSync},
		Code: &CodeVersionStatus{
			Recorded:      "v1",
			RecordedFound: true,
			NewerCount:    2,
		},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("stale code (newer archives on server) must block deploy")
	}
}

func TestNeedsForceToDeploy_EnvDriftIsBenign(t *testing.T) {
	// syncAppState merges env additive drift before upload, so the
	// deploy gate doesn't need to fire on it. Divergent env values
	// would error inside syncAppState rather than here.
	r := &DiffReport{
		YAML: SectionDiff{Status: StatusInSync},
		Env:  SectionDiff{Status: StatusDrift, AdditiveOnly: true},
	}
	if r.NeedsForceToDeploy() {
		t.Error("env drift is handled by syncAppState; gate should not block")
	}
}

func TestNeedsForceToDeploy_SecretAndOverrideDriftIsBenign(t *testing.T) {
	// Deploy doesn't push secret files or overrides. Drift in those
	// sections is handled by `apps sync`; the deploy gate must let it
	// through.
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusDrift, Entries: []SecretFileDiff{{Filename: "x", Status: StatusDrift}}},
		Overrides:   OverridesDiff{Status: StatusDrift, Entries: []OverrideDiff{{ID: "y", Status: StatusDrift}}},
	}
	if r.NeedsForceToDeploy() {
		t.Error("secret/override drift isn't deploy's concern; gate should not block")
	}
}

func TestNeedsForceToDeploy_AllInSyncReturnsFalse(t *testing.T) {
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		SecretEnv:   SectionDiff{Status: StatusInSync},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if r.NeedsForceToDeploy() {
		t.Error("clean state must not trigger the gate")
	}
}

// ---------------------------------------------------------------------------

func TestNeedsForceToOverwrite_DivergentEnvForces(t *testing.T) {
	r := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		Env:         SectionDiff{Status: StatusDrift, AdditiveOnly: false},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if !r.NeedsForceToOverwrite() {
		t.Error("force should be required when env diverges (not just additive)")
	}
}

// ---------------------------------------------------------------------------
// HasDrift
// ---------------------------------------------------------------------------

func TestDiffReport_HasDrift(t *testing.T) {
	allSync := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		SecretEnv:   SectionDiff{Status: StatusInSync},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if allSync.HasDrift() {
		t.Error("expected HasDrift=false when all sections in_sync")
	}

	yamlDrift := &DiffReport{
		YAML:        SectionDiff{Status: StatusDrift},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if !yamlDrift.HasDrift() {
		t.Error("expected HasDrift=true when yaml drifts")
	}

	secretsMissing := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusLocalMissing},
		Overrides:   OverridesDiff{Status: StatusInSync},
	}
	if !secretsMissing.HasDrift() {
		t.Error("expected HasDrift=true when secrets missing")
	}

	overrideDrift := &DiffReport{
		YAML:        SectionDiff{Status: StatusInSync},
		Env:         SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides:   OverridesDiff{Status: StatusDrift},
	}
	if !overrideDrift.HasDrift() {
		t.Error("expected HasDrift=true when overrides drift")
	}
}

// ---------------------------------------------------------------------------
// NeedsForceToOverwrite
// ---------------------------------------------------------------------------

func TestDiffReport_NeedsForceToOverwrite(t *testing.T) {
	// All in-sync: no force needed.
	r := &DiffReport{
		YAML: SectionDiff{Status: StatusInSync}, SecretEnv: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync}, Overrides: OverridesDiff{Status: StatusInSync},
	}
	if r.NeedsForceToOverwrite() {
		t.Error("no force expected when everything is in_sync")
	}

	// local_missing is NOT drift for pull; fresh writes are safe.
	r2 := &DiffReport{
		YAML:      SectionDiff{Status: StatusLocalMissing},
		SecretEnv: SectionDiff{Status: StatusLocalMissing},
		Env:       SectionDiff{Status: StatusLocalMissing},
		SecretFiles: SecretFilesDiff{Status: StatusLocalMissing, Entries: []SecretFileDiff{
			{Filename: "a", Status: StatusLocalMissing},
		}},
		Overrides: OverridesDiff{Status: StatusLocalMissing, Entries: []OverrideDiff{
			{ID: "o1", Status: StatusLocalMissing},
		}},
	}
	if r2.NeedsForceToOverwrite() {
		t.Error("local_missing should NOT require force (fresh write is safe)")
	}

	// YAML drift alone is enough to require force.
	r3 := &DiffReport{
		YAML: SectionDiff{Status: StatusDrift}, Env: SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync}, Overrides: OverridesDiff{Status: StatusInSync},
	}
	if !r3.NeedsForceToOverwrite() {
		t.Error("yaml drift must require force")
	}

	// A single drifting secret file triggers the gate even if the aggregate
	// status is drift + some entries are local_missing.
	r4 := &DiffReport{
		YAML: SectionDiff{Status: StatusInSync}, SecretEnv: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusDrift, Entries: []SecretFileDiff{
			{Filename: "a", Status: StatusLocalMissing},
			{Filename: "b", Status: StatusDrift},
		}},
		Overrides: OverridesDiff{Status: StatusInSync},
	}
	if !r4.NeedsForceToOverwrite() {
		t.Error("a single drifting secret file must require force")
	}

	// Same for overrides.
	r5 := &DiffReport{
		YAML: SectionDiff{Status: StatusInSync}, SecretEnv: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync},
		Overrides: OverridesDiff{Status: StatusDrift, Entries: []OverrideDiff{
			{ID: "o1", Status: StatusDrift},
		}},
	}
	if !r5.NeedsForceToOverwrite() {
		t.Error("a drifting override must require force")
	}
}

// ---------------------------------------------------------------------------
// BuildDiffReport (end-to-end against a fake conductor)
// ---------------------------------------------------------------------------

// fakeConductorForDiff stands up an httptest.Server that answers the four
// endpoints BuildDiffReport calls (app, env-vars, secret-files,
// overrides). routes can override individual paths; unmapped paths
// return 404.
func fakeConductorForDiff(t *testing.T, raw map[string]any, env map[string]string, secrets []SecretFileSummary, overrides []OverrideSummary) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/secret-env-vars"):
			writeJSON(t, w, 200, env)
		case strings.HasSuffix(path, "/env-vars"):
			// Plain env-vars (ConfigMap). Default to empty; tests that need
			// both populated should switch to a split helper.
			writeJSON(t, w, 200, map[string]string{})
		case strings.HasSuffix(path, "/secret-files"):
			writeJSON(t, w, 200, map[string]any{"files": secrets})
		case strings.HasSuffix(path, "/overrides"):
			if overrides == nil {
				writeJSON(t, w, 200, []OverrideSummary{})
			} else {
				writeJSON(t, w, 200, overrides)
			}
		case strings.HasSuffix(path, "/requires"):
			writeJSON(t, w, 200, map[string]ServiceRequirement{})
		default:
			// Treat anything else as the app GET.
			writeJSON(t, w, 200, raw)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestBuildDiffReport_404OnAppGetSurfacesAsTypedAPIError pins the
// I14-B-prerequisite: the apps.Service.get path now returns a typed
// *apps.APIError on non-2xx so callers (specifically the deploy
// drift-gate) can detect a 404 via errors.As and emit fresh-start
// recovery guidance instead of the generic "Warning: pre-deploy drift
// check failed; proceeding" line that left the user with no path
// forward when their app id had been deleted server-side.
func TestBuildDiffReport_404OnAppGetSurfacesAsTypedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every endpoint 404s — simulates the conductor's response when
		// the app id no longer exists. The drift-gate's first call is
		// GET /apps/:id; that's the one we want to surface.
		http.Error(w, `{"error":"App 'appid1' not found","statusCode":404}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")
	local := &PulledApp{App: "svc", DeployType: "cli", ID: "appid1", CID: "k1", AID: "acc-1", Replicas: 1, ServicePortMappings: []Port{{Port: 80, StandardHttps: true}}}

	_, err := BuildDiffReport(svc, local, "/tmp/x.yaml", "acc-1", "k1")
	if err == nil {
		t.Fatal("expected 404 error from BuildDiffReport, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError in the wrap chain, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, http.StatusNotFound)
	}
}

func TestBuildDiffReport_AccountMismatch(t *testing.T) {
	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-A"}
	svc := NewService("http://unused", "tok", "k1", "acc-A")
	_, err := BuildDiffReport(svc, localApp, "/tmp/whatever.yaml", "acc-DIFFERENT", "k1")
	if err == nil {
		t.Fatal("expected account mismatch error")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("error should mention account; got: %v", err)
	}
}

func TestBuildDiffReport_ClusterMismatch(t *testing.T) {
	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1"}
	svc := NewService("http://unused", "tok", "k2", "acc-1")
	_, err := BuildDiffReport(svc, localApp, "/tmp/whatever.yaml", "acc-1", "k2")
	if err == nil {
		t.Fatal("expected cluster mismatch error")
	}
	if !strings.Contains(err.Error(), "cluster mismatch") {
		t.Errorf("error should mention cluster mismatch; got: %v", err)
	}
}

func TestBuildDiffReport_AllInSync(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"id":                         "ab12c",
		"name":                       "web",
		"replicas":                   float64(2),
		"resourceRequirementClassId": "app.sl1.beff",
	}

	// Local yaml = what BuildServerStateForDiff would produce, so bytes
	// match exactly when the diff runs.
	serverState := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, err := yaml.Marshal(serverState)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	srv := fakeConductorForDiff(t, raw, nil, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.HasDrift() {
		t.Errorf("expected no drift; got report:\n  yaml=%s\n  unifiedDiff=%s", report.YAML.Status, report.YAML.UnifiedDiff)
	}
}

func TestBuildDiffReport_YamlDriftWhenReplicasDiffer(t *testing.T) {
	dir := t.TempDir()

	// Local has replicas=1.
	localState := BuildServerStateForDiff(map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(localState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Server says replicas=5.
	srv := fakeConductorForDiff(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(5),
	}, nil, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if !report.HasDrift() {
		t.Fatal("expected drift when replicas differ")
	}
	if report.YAML.Status != StatusDrift {
		t.Errorf("yaml status = %q, want drift", report.YAML.Status)
	}
	if !strings.Contains(report.YAML.UnifiedDiff, "replicas") {
		t.Errorf("unified diff should mention replicas; got:\n%s", report.YAML.UnifiedDiff)
	}
}

// I3-E regression: a freshly-provisioned app has platform-injected
// secret env vars (DATABASE_URL, REDIS_*, etc.) on the server
// immediately. The user's local `.secret.env` doesn't carry those
// values until they explicitly run `apps_pull`. Pre-fix, the drift
// gate compared them anyway and refused to deploy. The filter
// (FilterPlatformInjectedEnv applied inside BuildDiffReport) drops
// any server key that appears as a value in some
// requires.<alias>.env, so the diff is over user-set keys only.
func TestBuildDiffReport_FiltersPlatformInjectedSecretEnv(t *testing.T) {
	dir := t.TempDir()

	raw := map[string]any{
		"id":       "appid2",
		"name":     "pasteapp",
		"replicas": float64(1),
	}

	// Server returns DATABASE_URL (platform-injected) + USER_TOKEN
	// (user-set). Local file has only USER_TOKEN.
	serverSecretEnv := map[string]string{
		"DATABASE_URL": "postgresql://platform-injected/...",
		"USER_TOKEN":   "user-set-value",
	}
	requires := map[string]ServiceRequirement{
		"db": {Type: "postgresql", ID: "pug3s", Env: map[string]string{"url": "DATABASE_URL"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/secret-env-vars"):
			writeJSON(t, w, 200, serverSecretEnv)
		case strings.HasSuffix(r.URL.Path, "/env-vars"):
			writeJSON(t, w, 200, map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/secret-files"):
			writeJSON(t, w, 200, map[string]any{"files": []SecretFileSummary{}})
		case strings.HasSuffix(r.URL.Path, "/overrides"):
			writeJSON(t, w, 200, []OverrideSummary{})
		case strings.HasSuffix(r.URL.Path, "/requires"):
			writeJSON(t, w, 200, requires)
		default:
			writeJSON(t, w, 200, raw)
		}
	}))
	t.Cleanup(srv.Close)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	// Local yaml is the rendered server-state PulledApp (no drift on
	// yaml side) and a `.secret.env` containing only the user's key.
	localState := BuildServerStateForDiff(raw, "k1", "acc-1", serverSecretEnv, nil, nil, nil, requires)
	localState.SecretEnv = ".secret.env"
	yamlBytes, _ := yaml.Marshal(localState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret.env"), RenderEnvBytes(map[string]string{"USER_TOKEN": "user-set-value"}), 0600); err != nil {
		t.Fatalf("write secret env: %v", err)
	}

	localApp := &PulledApp{
		ID: "appid2", CID: "k1", AID: "acc-1", App: "pasteapp",
		SecretEnv: ".secret.env",
		Requires:  requires,
	}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.SecretEnv.Status != StatusInSync {
		t.Errorf("secret env status = %q, want in_sync (DATABASE_URL is platform-injected and must drop out of the comparison)", report.SecretEnv.Status)
	}
}

// I3-E partner: when the local `.secret.env` lacks a non-injected
// (user-set) key the server has, drift IS still surfaced. Filtering
// must not over-mask: real divergence on user keys still trips the
// section.
func TestBuildDiffReport_RealUserSecretDriftStillSurfaced(t *testing.T) {
	dir := t.TempDir()

	raw := map[string]any{
		"id":       "appid2",
		"name":     "pasteapp",
		"replicas": float64(1),
	}

	serverSecretEnv := map[string]string{
		"DATABASE_URL": "postgresql://platform-injected/...",
		"USER_TOKEN":   "user-set-value",
		"NEW_KEY":      "server-only", // server has, local doesn't
	}
	requires := map[string]ServiceRequirement{
		"db": {Type: "postgresql", ID: "pug3s", Env: map[string]string{"url": "DATABASE_URL"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/secret-env-vars"):
			writeJSON(t, w, 200, serverSecretEnv)
		case strings.HasSuffix(r.URL.Path, "/env-vars"):
			writeJSON(t, w, 200, map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/secret-files"):
			writeJSON(t, w, 200, map[string]any{"files": []SecretFileSummary{}})
		case strings.HasSuffix(r.URL.Path, "/overrides"):
			writeJSON(t, w, 200, []OverrideSummary{})
		case strings.HasSuffix(r.URL.Path, "/requires"):
			writeJSON(t, w, 200, requires)
		default:
			writeJSON(t, w, 200, raw)
		}
	}))
	t.Cleanup(srv.Close)
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localState := BuildServerStateForDiff(raw, "k1", "acc-1", serverSecretEnv, nil, nil, nil, requires)
	localState.SecretEnv = ".secret.env"
	yamlBytes, _ := yaml.Marshal(localState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// Local has USER_TOKEN only; missing NEW_KEY (a non-injected user key).
	if err := os.WriteFile(filepath.Join(dir, ".secret.env"), RenderEnvBytes(map[string]string{"USER_TOKEN": "user-set-value"}), 0600); err != nil {
		t.Fatalf("write secret env: %v", err)
	}

	localApp := &PulledApp{
		ID: "appid2", CID: "k1", AID: "acc-1", App: "pasteapp",
		SecretEnv: ".secret.env",
		Requires:  requires,
	}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.SecretEnv.Status != StatusDrift {
		t.Errorf("secret env status = %q, want drift (NEW_KEY is user-set and missing locally)", report.SecretEnv.Status)
	}
	if !report.SecretEnv.AdditiveOnly {
		t.Errorf("additive-only should be true (server has user key local doesn't), got false")
	}
}

// I3-B regression: a user-explicit `secretEnv:` / `env:` path on the
// local yaml must not surface as yaml drift just because
// BuildServerStateForDiff stamped the canonical default. The merge
// inside BuildDiffReport copies the user's value into serverState
// before the byte-comparison.
func TestBuildDiffReport_PreservesUserAuthoredEnvPaths(t *testing.T) {
	dir := t.TempDir()

	raw := map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}
	envVars := map[string]string{"USER_TOKEN": "x"}

	srv := fakeConductorForDiff(t, raw, envVars, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	// User authored `secretEnv: .secret.env` and `env: plain.env`,
	// not the canonical defaults. Start from the server-rendered
	// PulledApp shape (so all the default-stamped fields like
	// cpuRequestMc, deployType, etc. match) and only override the
	// env path fields.
	localApp := BuildServerStateForDiff(raw, "k1", "acc-1", map[string]string{"USER_TOKEN": "x"}, envVars, nil, nil, nil)
	localApp.SecretEnv = ".secret.env"
	localApp.Env = "plain.env"
	yamlBytes, _ := yaml.Marshal(localApp)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// Match the env content the server returned so env section is in_sync.
	if err := os.WriteFile(filepath.Join(dir, ".secret.env"), RenderEnvBytes(envVars), 0600); err != nil {
		t.Fatalf("write secret env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.env"), RenderEnvBytes(map[string]string{}), 0644); err != nil {
		t.Fatalf("write plain env: %v", err)
	}

	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	// Yaml must be in_sync: user-explicit secretEnv: .secret.env
	// should win over canonical default in the rendered server state.
	if report.YAML.Status != StatusInSync {
		t.Errorf("yaml status = %q, want in_sync (user-authored env paths must merge into server state); diff=%s",
			report.YAML.Status, report.YAML.UnifiedDiff)
	}
}

func TestBuildDiffReport_SecretEnvDriftWhenServerHasExtra(t *testing.T) {
	dir := t.TempDir()

	// Local yaml has no env block; the secret env file holds A=1 only.
	localState := BuildServerStateForDiff(map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1", map[string]string{"A": "1"}, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(localState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), RenderEnvBytes(map[string]string{"A": "1"}), 0600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	// Server has secret env A=1 + B=server-only.
	serverEnv := map[string]string{"A": "1", "B": "server-only"}
	srv := fakeConductorForDiff(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, serverEnv, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web", SecretEnv: ".env"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if !report.HasDrift() {
		t.Fatal("expected drift (secret env section)")
	}
	if report.SecretEnv.Status != StatusDrift {
		t.Errorf("secret env status = %q, want drift", report.SecretEnv.Status)
	}
	if !report.SecretEnv.AdditiveOnly {
		t.Errorf("secret env drift should be additive-only when server has extra keys; got AdditiveOnly=%v", report.SecretEnv.AdditiveOnly)
	}
}

// Regression test for V3 (VCS_DEPLOY_TEST_NOTES.md): when the local yaml
// omits the `env:` field, BuildDiffReport must still honour the documented
// default path (`runos.<cid>.<id>.config.env`). Pre-fix: the env section
// was gated on `len(envVars) > 0`, so a server with zero env vars + a
// local file at the default path with content was silently reported
// `in_sync`, hiding the drift.
func TestBuildDiffReport_LocalEnvAtDefaultPath_NoYamlField_ReportsDrift(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}

	// Local yaml has NO `env:` field. The server-state-shaped yaml has
	// nothing under env either (server returns 0 plain env vars).
	serverState := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(serverState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// Local file at the documented default path with two vars.
	defaultEnvPath := filepath.Join(dir, EnvFilename("k1", "ab12c"))
	if err := os.WriteFile(defaultEnvPath, RenderEnvBytes(map[string]string{"APP_NAME": "aliens", "LOG_LEVEL": "debug"}), 0644); err != nil {
		t.Fatalf("write env: %v", err)
	}

	// fakeConductorForDiff returns {} on /env-vars, which is exactly what
	// V3's "server has nothing" scenario looks like.
	srv := fakeConductorForDiff(t, raw, nil, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"} // Env intentionally empty
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.Env.Status != StatusDrift {
		t.Errorf("env status = %q, want %q (V3: local file at default path must surface as drift, not in_sync)", report.Env.Status, StatusDrift)
	}
	if report.Env.Path == "" {
		t.Error("env.Path empty: V3 fix must populate the resolved default path on the report so users see which file was compared")
	} else if filepath.Base(report.Env.Path) != EnvFilename("k1", "ab12c") {
		t.Errorf("env.Path = %q, want leaf %q", report.Env.Path, EnvFilename("k1", "ab12c"))
	}
}

// Regression test for V3, secret-env side. Same shape as the plain-env
// test above: yaml omits `secretEnv:`, file exists at default path, server
// returns zero secret env vars; expect drift surfaced with a populated path.
func TestBuildDiffReport_LocalSecretEnvAtDefaultPath_NoYamlField_ReportsDrift(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}

	serverState := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(serverState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	defaultSecretPath := filepath.Join(dir, SecretEnvFilename("k1", "ab12c"))
	if err := os.WriteFile(defaultSecretPath, RenderEnvBytes(map[string]string{"API_TOKEN": "shh"}), 0600); err != nil {
		t.Fatalf("write secret env: %v", err)
	}

	// Pass nil for env -> /secret-env-vars also returns nil, so the server
	// side is empty. The V3-fixed code must still spot the local file at
	// the default secret path.
	srv := fakeConductorForDiff(t, raw, nil, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"} // SecretEnv intentionally empty
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.SecretEnv.Status != StatusDrift {
		t.Errorf("secretEnv status = %q, want %q (V3: local file at default path must surface as drift)", report.SecretEnv.Status, StatusDrift)
	}
	if report.SecretEnv.Path == "" {
		t.Error("secretEnv.Path empty: V3 fix must populate the resolved default path")
	} else if filepath.Base(report.SecretEnv.Path) != SecretEnvFilename("k1", "ab12c") {
		t.Errorf("secretEnv.Path = %q, want leaf %q", report.SecretEnv.Path, SecretEnvFilename("k1", "ab12c"))
	}
}

// ---------------------------------------------------------------------------
// BuildDiffReport + Code section
// ---------------------------------------------------------------------------

func TestBuildDiffReport_PopulatesCodeSectionFromSidecar(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}
	state := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(state)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := WriteSourceVersion(dir, "k1", "ab12c", "v1"); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}

	// Stand up a server that returns the raw app + a non-empty archive
	// list with two newer entries than v1.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cli-archives"):
			writeJSON(t, w, 200, cliArchivesEnvelope{
				CID:     "k1",
				AppID:   "ab12c",
				AppName: "web",
				Archives: []CliArchive{
					{CliUploadID: "v1", PushTime: "2026-04-26T10:00:00Z"},
					{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
					{CliUploadID: "v3", PushTime: "2026-04-28T10:00:00Z"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/secret-env-vars"):
			writeJSON(t, w, 200, map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/env-vars"):
			writeJSON(t, w, 200, map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/secret-files"):
			writeJSON(t, w, 200, map[string]any{"files": []any{}})
		case strings.HasSuffix(r.URL.Path, "/overrides"):
			writeJSON(t, w, 200, []OverrideSummary{})
		case strings.HasSuffix(r.URL.Path, "/requires"):
			writeJSON(t, w, 200, map[string]ServiceRequirement{})
		default:
			writeJSON(t, w, 200, raw)
		}
	}))
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.Code == nil {
		t.Fatal("expected Code section to be populated when sidecar exists")
	}
	if report.Code.Recorded != "v1" {
		t.Errorf("Code.Recorded = %q; want v1", report.Code.Recorded)
	}
	if !report.Code.IsStale() || report.Code.NewerCount != 2 {
		t.Errorf("expected stale with 2 newer; got %+v", report.Code)
	}
	if !report.HasDrift() {
		t.Error("HasDrift() should be true when only the code section is stale")
	}
}

func TestBuildDiffReport_NoSidecarOmitsCodeSection(t *testing.T) {
	dir := t.TempDir()
	raw := map[string]any{"id": "ab12c", "name": "web", "replicas": float64(1)}
	state := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(state)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	// No sidecar.

	srv := fakeConductorForDiff(t, raw, nil, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if report.Code != nil {
		t.Errorf("Code should be nil when no sidecar exists; got %+v", report.Code)
	}
	if report.HasDrift() {
		t.Error("HasDrift() should be false when nothing differs and no sidecar")
	}
}

// ---------------------------------------------------------------------------
// JSON shape: SectionDiff.AdditiveOnly always present
// ---------------------------------------------------------------------------

func TestSectionDiff_JSONAlwaysIncludesAdditiveOnly(t *testing.T) {
	// LLM callers parsing the diff JSON shouldn't have to infer
	// "additiveOnly absent == false". The field must always serialize.
	cases := []SectionDiff{
		{Status: StatusInSync},
		{Status: StatusDrift, AdditiveOnly: false},
		{Status: StatusDrift, AdditiveOnly: true},
	}
	for _, sd := range cases {
		blob, err := json.Marshal(sd)
		if err != nil {
			t.Fatalf("marshal %+v: %v", sd, err)
		}
		if !strings.Contains(string(blob), `"additiveOnly":`) {
			t.Errorf("expected additiveOnly key in JSON for %+v; got %s", sd, blob)
		}
	}
}

// ---------------------------------------------------------------------------
// listServerOnlyFields — drives the deploy gate's --force deletion warning
// ---------------------------------------------------------------------------

func TestListServerOnlyFields_TopLevel(t *testing.T) {
	local := []byte(`app: x
replicas: 1
`)
	server := []byte(`app: x
replicas: 1
clusterDomainId: elpfn
healthCheck: standard
`)
	got := listServerOnlyFields(local, server)
	want := map[string]bool{
		`clusterDomainId ("elpfn")`:  true,
		`healthCheck ("standard")`:   true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected entry: %q", g)
		}
	}
}

func TestListServerOnlyFields_NestedArrayOfMaps(t *testing.T) {
	// Reproduces the T29 case: server has servicePortMappings[0].domains
	// that local doesn't. The walker must surface the nested path.
	local := []byte(`servicePortMappings:
    - port: 8080
      standardHttps: true
`)
	server := []byte(`servicePortMappings:
    - port: 8080
      standardHttps: true
      domains:
        - fqdn: hve1.mercatura.co.za
          enableCloudflareProxy: true
        - fqdn: hve2.mercatura.co.za
`)
	got := listServerOnlyFields(local, server)
	want := "servicePortMappings[0].domains (2 entries)"
	found := false
	for _, g := range got {
		if g == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in output; got %v", want, got)
	}
}

func TestListServerOnlyFields_EqualReturnsEmpty(t *testing.T) {
	body := []byte(`app: x
replicas: 1
servicePortMappings:
    - port: 8080
`)
	got := listServerOnlyFields(body, body)
	if len(got) != 0 {
		t.Errorf("expected empty list when sides match; got %v", got)
	}
}

func TestListServerOnlyFields_UnparseableInputs(t *testing.T) {
	// Defensive: malformed yaml on either side should yield nil rather
	// than a panic — caller treats nil as "couldn't determine, fall
	// back to the generic diff output".
	if got := listServerOnlyFields([]byte("\t\tnot-yaml"), []byte("app: x\n")); got != nil {
		t.Errorf("expected nil on malformed local; got %v", got)
	}
	if got := listServerOnlyFields([]byte("app: x\n"), []byte("[unclosed")); got != nil {
		t.Errorf("expected nil on malformed server; got %v", got)
	}
}

func TestComputeYAMLDiff_AdditivePopulatesServerOnlyFields(t *testing.T) {
	// End-to-end: when the diff is additive, the SectionDiff carries
	// the server-only-fields list so the deploy gate can render it.
	dir := t.TempDir()
	local := &PulledApp{
		App: "svc", DeployType: "cli", ID: "x", CID: "k1", AID: "acc-1",
		Replicas: 1,
	}
	localBytes, _ := yaml.Marshal(local)
	path := filepath.Join(dir, "svc.yaml")
	_ = os.WriteFile(path, localBytes, 0644)

	server := *local
	server.ClusterDomainID = "elpfn"
	server.HealthCheck = "standard"

	got, err := ComputeYAMLDiff(path, &server)
	if err != nil {
		t.Fatalf("ComputeYAMLDiff: %v", err)
	}
	if got.Status != StatusDrift {
		t.Fatalf("status = %q, want drift", got.Status)
	}
	if !got.AdditiveOnly {
		t.Fatalf("expected AdditiveOnly=true (server adds two top-level keys)")
	}
	if len(got.ServerOnlyFields) != 2 {
		t.Errorf("expected 2 ServerOnlyFields, got %d: %v", len(got.ServerOnlyFields), got.ServerOnlyFields)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// RedactEnvUnifiedDiff
// ---------------------------------------------------------------------------

// I5-F regression: round-trip-only yaml fields (sourceDir,
// dockerfile) are local-tooling state — the push paths ignore them,
// so the diff path must too. Pre-fix a freshly subdir-mode-pulled
// workspace exited 2 on `runos apps diff` because the `sourceDir: ..`
// stamp showed as drift even though deploy would push exactly zero
// changes.
func TestIsRoundTripOnlyYAMLField(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"sourceDir", true},
		{"dockerfile", true},
		// Non-round-trip fields stay actionable.
		{"replicas", false},
		{"cpuRequestMc", false},
		{"healthCheckPort", false},
		{"requires", false},
		// Partial / suffix match must NOT trigger.
		{"sourceDirOther", false},
		{"DockerfileX", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRoundTripOnlyYAMLField(c.name); got != c.want {
				t.Errorf("IsRoundTripOnlyYAMLField(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestFilterRoundTripFromYAMLDiff_FoldsPureWaiverToInSync(t *testing.T) {
	sd := SectionDiff{
		Status: StatusDrift,
		Path:   "/tmp/runos.yaml",
		UnifiedDiff: "--- local\n+++ server\n@@\n aid: myacct\n-sourceDir: ..\n replicas: 1\n",
		ServerOnlyFields: []string{`sourceDir ("..")`},
	}
	got := FilterRoundTripFromYAMLDiff(sd)
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync (sourceDir is the only delta and it's waivered)", got.Status)
	}
	if got.UnifiedDiff != "" {
		t.Errorf("unified diff should be empty after fold, got %q", got.UnifiedDiff)
	}
	if len(got.ServerOnlyFields) != 0 {
		t.Errorf("ServerOnlyFields should be empty, got %v", got.ServerOnlyFields)
	}
}

func TestFilterRoundTripFromYAMLDiff_KeepsActionableLines(t *testing.T) {
	// Mixed: sourceDir + a real replicas change. Should keep replicas
	// in the body, drop sourceDir, stay in drift state.
	sd := SectionDiff{
		Status: StatusDrift,
		Path:   "/tmp/runos.yaml",
		UnifiedDiff: "--- local\n+++ server\n@@\n aid: myacct\n-sourceDir: ..\n-replicas: 1\n+replicas: 2\n",
		ServerOnlyFields: []string{`sourceDir ("..")`, "replicas (2)"},
	}
	got := FilterRoundTripFromYAMLDiff(sd)
	if got.Status != StatusDrift {
		t.Errorf("status = %q, want drift (replicas mismatch is actionable)", got.Status)
	}
	if strings.Contains(got.UnifiedDiff, "sourceDir") {
		t.Errorf("sourceDir should be removed from unified diff, got %q", got.UnifiedDiff)
	}
	if !strings.Contains(got.UnifiedDiff, "replicas") {
		t.Errorf("replicas should remain in unified diff, got %q", got.UnifiedDiff)
	}
	if len(got.ServerOnlyFields) != 1 || got.ServerOnlyFields[0] != "replicas (2)" {
		t.Errorf("ServerOnlyFields = %v, want only replicas entry", got.ServerOnlyFields)
	}
}

func TestFilterRoundTripFromYAMLDiff_InSyncPassthrough(t *testing.T) {
	sd := SectionDiff{Status: StatusInSync, Path: "/tmp/runos.yaml"}
	got := FilterRoundTripFromYAMLDiff(sd)
	if got.Status != StatusInSync {
		t.Errorf("in_sync input must pass through unchanged, got %q", got.Status)
	}
}

// I5-F secret env side: platform-injected names must drop out of
// the comparison on BOTH local and server (the conductor's I4-C/E
// push-side filter strips them from anything the CLI sends, so a
// local file that carries them isn't actionable). Pre-fix the iter-3
// R1 filter dropped them server-side only — local-has + server-doesn't
// still showed as drift, exit 2.
func TestComputeEnvDiffFiltered_SymmetricStrip(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, ".secret.env")
	// Local has user-set keys + 4 platform-injected names.
	localContent := []byte("ADMIN_TOKEN=tok\nDATABASE_URL=postgresql://...\nJWT_SECRET=jwt\nREDIS_HOST=valkey\nREDIS_PASSWORD=pw\nREDIS_PORT=6379\n")
	if err := os.WriteFile(localPath, localContent, 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	// Server has only the user-set keys (the conductor's push-side
	// filter stripped the platform-injected ones from the previous
	// deploy's customSecretEnvVars).
	server := map[string]string{
		"ADMIN_TOKEN": "tok",
		"JWT_SECRET":  "jwt",
	}
	platform := map[string]bool{
		"DATABASE_URL":   true,
		"REDIS_HOST":     true,
		"REDIS_PASSWORD": true,
		"REDIS_PORT":     true,
	}
	got, err := ComputeEnvDiffFiltered(localPath, server, platform)
	if err != nil {
		t.Fatalf("ComputeEnvDiffFiltered: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("status = %q, want in_sync (only platform-injected names differ; pure waiver-class drift)", got.Status)
	}
}

func TestComputeEnvDiffFiltered_RealUserDriftStillSurfaces(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, ".secret.env")
	// Local missing JWT_SECRET (real user-key drift), has DATABASE_URL
	// (platform-injected, must drop out of comparison).
	if err := os.WriteFile(localPath, []byte("ADMIN_TOKEN=tok\nDATABASE_URL=foo\n"), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	server := map[string]string{
		"ADMIN_TOKEN": "tok",
		"JWT_SECRET":  "jwt", // server has user key local doesn't
	}
	platform := map[string]bool{"DATABASE_URL": true}
	got, err := ComputeEnvDiffFiltered(localPath, server, platform)
	if err != nil {
		t.Fatalf("ComputeEnvDiffFiltered: %v", err)
	}
	if got.Status != StatusDrift {
		t.Errorf("status = %q, want drift (JWT_SECRET is user-set and missing locally)", got.Status)
	}
	if !got.AdditiveOnly {
		t.Errorf("additive-only should be true (server has user key local doesn't), got false")
	}
}

func TestComputeEnvDiffFiltered_EmptyPlatformDelegatesToComputeEnvDiff(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(localPath, []byte("A=1\n"), 0644); err != nil {
		t.Fatalf("write local: %v", err)
	}
	got, err := ComputeEnvDiffFiltered(localPath, map[string]string{"A": "1"}, nil)
	if err != nil {
		t.Fatalf("ComputeEnvDiffFiltered: %v", err)
	}
	if got.Status != StatusInSync {
		t.Errorf("identical local + server with no platform set must be in_sync, got %q", got.Status)
	}
}

// I4-B regression: deploy drift gate must not leak raw secret env
// values into stdout, since CI logs persist them. RedactSecrets
// rewrites the secret-env unified diff to <redacted> markers and
// strips secret-file unified diffs entirely. Plain env stays
// readable (committed to VCS by definition, not sensitive).
func TestRedactSecrets_RewritesSecretSectionsOnly(t *testing.T) {
	report := &DiffReport{
		SecretEnv: SectionDiff{
			Status:      StatusDrift,
			UnifiedDiff: "--- local\n+++ server\n@@\n-DATABASE_URL=postgresql://leak/me\n+DATABASE_URL=postgresql://from/server\n",
		},
		Env: SectionDiff{
			Status:      StatusDrift,
			UnifiedDiff: "--- local\n+++ server\n@@\n-LOG_LEVEL=info\n+LOG_LEVEL=debug\n",
		},
		SecretFiles: SecretFilesDiff{
			Entries: []SecretFileDiff{
				{Filename: "tls.key", UnifiedDiff: "-----BEGIN PRIVATE KEY-----\nleak-this\n"},
			},
		},
	}
	report.RedactSecrets()

	if strings.Contains(report.SecretEnv.UnifiedDiff, "leak/me") || strings.Contains(report.SecretEnv.UnifiedDiff, "from/server") {
		t.Errorf("secret env values must be redacted, got: %s", report.SecretEnv.UnifiedDiff)
	}
	if !strings.Contains(report.SecretEnv.UnifiedDiff, "<redacted>") {
		t.Errorf("secret env diff should contain <redacted> markers, got: %s", report.SecretEnv.UnifiedDiff)
	}
	// Plain env values are committed to VCS; the gate keeps them readable.
	if !strings.Contains(report.Env.UnifiedDiff, "LOG_LEVEL=info") {
		t.Errorf("plain env values must NOT be redacted, got: %s", report.Env.UnifiedDiff)
	}
	// Secret-file unified diffs are stripped entirely (the file's
	// bytes can be anything, redaction by line shape isn't reliable).
	if report.SecretFiles.Entries[0].UnifiedDiff != "" {
		t.Errorf("secret-file unified diffs must be cleared, got: %q", report.SecretFiles.Entries[0].UnifiedDiff)
	}
}

// I4-B partner: nil-safe.
func TestRedactSecrets_NilSafe(t *testing.T) {
	var r *DiffReport
	r.RedactSecrets() // must not panic on nil receiver
}

func TestRedactEnvUnifiedDiff_EmptyInput(t *testing.T) {
	if got := RedactEnvUnifiedDiff(""); got != "" {
		t.Errorf("RedactEnvUnifiedDiff(\"\") = %q, want \"\"", got)
	}
}

func TestRedactEnvUnifiedDiff_RedactsAddedAndRemovedValues(t *testing.T) {
	in := `--- local
+++ server
@@ -1,2 +1,2 @@
-API_KEY=oldsecret
+API_KEY=newsecret
 SHARED=common
`
	got := RedactEnvUnifiedDiff(in)
	// Values must be replaced.
	if strings.Contains(got, "oldsecret") || strings.Contains(got, "newsecret") || strings.Contains(got, "common") {
		t.Errorf("redacted diff still contains a value: %q", got)
	}
	// Keys and markers must survive.
	for _, want := range []string{
		"-API_KEY=<redacted>",
		"+API_KEY=<redacted>",
		" SHARED=<redacted>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted diff missing %q\nfull:\n%s", want, got)
		}
	}
	// Headers must pass through unchanged.
	for _, want := range []string{"--- local", "+++ server", "@@ -1,2 +1,2 @@"} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted diff lost header %q\nfull:\n%s", want, got)
		}
	}
}

func TestRedactEnvUnifiedDiff_NonEnvLinesUnchanged(t *testing.T) {
	in := `@@ -1,1 +1,1 @@
 not-an-env-line
+plain-add
-plain-remove
`
	got := RedactEnvUnifiedDiff(in)
	for _, want := range []string{" not-an-env-line", "+plain-add", "-plain-remove"} {
		if !strings.Contains(got, want) {
			t.Errorf("non-env line %q was modified\nfull:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<redacted>") {
		t.Errorf("non-env diff should not contain <redacted>: %q", got)
	}
}

func TestRedactEnvUnifiedDiff_PreservesEqualsInValueOnly(t *testing.T) {
	// Value containing `=` must still be redacted (only the FIRST `=` splits).
	in := "+TOKEN=base64==stuff\n"
	got := RedactEnvUnifiedDiff(in)
	if strings.Contains(got, "stuff") || strings.Contains(got, "base64") {
		t.Errorf("expected entire value redacted, got %q", got)
	}
	if !strings.Contains(got, "+TOKEN=<redacted>") {
		t.Errorf("expected +TOKEN=<redacted>, got %q", got)
	}
}
