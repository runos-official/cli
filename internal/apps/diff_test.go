package apps

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

	p := BuildServerStateForDiff(raw, "k1", "acc-1", envs, secretFiles, nil, nil)

	if p.Env != ".runos.k1.ab12c.env" {
		t.Errorf("Env = %q, want .runos.k1.ab12c.env (envs present)", p.Env)
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
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, requires)
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
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, requires)
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
	p := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil)
	if p.Requires != nil {
		t.Errorf("nil deps must leave Requires nil; got %+v", p.Requires)
	}
}

func TestBuildServerStateForDiff_OmitsEnvWhenEmpty(t *testing.T) {
	raw := map[string]any{"id": "x", "name": "svc", "replicas": float64(1)}
	p := BuildServerStateForDiff(raw, "k1", "acc-1", map[string]string{}, nil, nil, nil)
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

func TestNeedsForceToDeploy_ServerHasFieldsLocalDoesntBlocks(t *testing.T) {
	// Server has fields local doesn't. Under the new conductor's
	// omit-equals-clear rule for the 5 desired-state fields, this
	// could mean a silent deletion. Even when the server-only fields
	// are partial-update (preserved on omit), the gate is conservative
	// and refuses without --force so the user takes an explicit step.
	r := &DiffReport{
		YAML: SectionDiff{Status: StatusDrift, AdditiveOnly: true},
	}
	if !r.NeedsForceToDeploy() {
		t.Error("server-only fields must block deploy without --force")
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
		YAML: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
		SecretFiles: SecretFilesDiff{Status: StatusInSync}, Overrides: OverridesDiff{Status: StatusInSync},
	}
	if r.NeedsForceToOverwrite() {
		t.Error("no force expected when everything is in_sync")
	}

	// local_missing is NOT drift for pull; fresh writes are safe.
	r2 := &DiffReport{
		YAML: SectionDiff{Status: StatusLocalMissing},
		Env:  SectionDiff{Status: StatusLocalMissing},
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
		YAML: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
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
		YAML: SectionDiff{Status: StatusInSync}, Env: SectionDiff{Status: StatusInSync},
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
		case strings.HasSuffix(path, "/env-vars"):
			writeJSON(t, w, 200, env)
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
	serverState := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil)
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
	}, "k1", "acc-1", nil, nil, nil, nil)
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

func TestBuildDiffReport_EnvDriftWhenServerHasExtra(t *testing.T) {
	dir := t.TempDir()

	// Local yaml has no env block; the env file holds A=1 only.
	localState := BuildServerStateForDiff(map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, "k1", "acc-1", map[string]string{"A": "1"}, nil, nil, nil)
	yamlBytes, _ := yaml.Marshal(localState)
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, yamlBytes, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), RenderEnvBytes(map[string]string{"A": "1"}), 0600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	// Server has env A=1 + B=server-only.
	serverEnv := map[string]string{"A": "1", "B": "server-only"}
	srv := fakeConductorForDiff(t, map[string]any{
		"id":       "ab12c",
		"name":     "web",
		"replicas": float64(1),
	}, serverEnv, nil, nil)
	defer srv.Close()
	svc := NewService(srv.URL, "tok", "k1", "acc-1")

	localApp := &PulledApp{ID: "ab12c", CID: "k1", AID: "acc-1", App: "web", Env: ".env"}
	report, err := BuildDiffReport(svc, localApp, yamlPath, "acc-1", "k1")
	if err != nil {
		t.Fatalf("BuildDiffReport: %v", err)
	}
	if !report.HasDrift() {
		t.Fatal("expected drift (env section)")
	}
	if report.Env.Status != StatusDrift {
		t.Errorf("env status = %q, want drift", report.Env.Status)
	}
	if !report.Env.AdditiveOnly {
		t.Errorf("env drift should be additive-only when server has extra keys; got AdditiveOnly=%v", report.Env.AdditiveOnly)
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
	state := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil)
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
			writeJSON(t, w, 200, []CliArchive{
				{CliUploadID: "v1", PushTime: "2026-04-26T10:00:00Z"},
				{CliUploadID: "v2", PushTime: "2026-04-27T10:00:00Z"},
				{CliUploadID: "v3", PushTime: "2026-04-28T10:00:00Z"},
			})
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
	state := BuildServerStateForDiff(raw, "k1", "acc-1", nil, nil, nil, nil)
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
