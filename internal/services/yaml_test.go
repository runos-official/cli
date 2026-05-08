package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename("mycluster3", "postgresql", "abc12"))

	original := &ServiceYAML{
		Type: "postgresql",
		ID:   "abc12",
		CID:  "mycluster3",
		AID:  "acct1",
		Fields: map[string]any{
			"name":                       "poll-app-db",
			"resourceRequirementClassId": "postgresql.c0.beff",
			"version":                    "17.6",
			"replicas":                   1,
			"nodeAffinityTagIds":         []any{"tag1", "tag2"},
		},
	}
	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Type != original.Type || loaded.ID != original.ID || loaded.CID != original.CID || loaded.AID != original.AID {
		t.Fatalf("header mismatch: got %+v", loaded)
	}
	if got, want := loaded.Fields["name"], original.Fields["name"]; got != want {
		t.Errorf("name field: got %v, want %v", got, want)
	}
	// yaml.v3 unmarshals plain integers into int, not int64; round-trip
	// the value via fmt to compare without locking on the exact type.
	if got, want := loaded.Fields["version"], "17.6"; got != want {
		t.Errorf("version field: got %v, want %v", got, want)
	}
}

func TestServiceYAMLHeaderOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFilename("mycluster3", "postgresql", "abc12"))
	y := &ServiceYAML{
		Type: "postgresql",
		ID:   "abc12",
		CID:  "mycluster3",
		AID:  "acct1",
		Fields: map[string]any{
			"name":     "x",
			"replicas": 1,
		},
	}
	if err := Save(path, y); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// Header keys must come before any inline-map key. yaml.v3 preserves
	// struct field order, so type/id/cid/aid all precede the alphabetised
	// inline keys (name, replicas).
	idxType := strings.Index(got, "type:")
	idxName := strings.Index(got, "name:")
	if idxType < 0 || idxName < 0 || idxType > idxName {
		t.Errorf("expected header before inline keys, got:\n%s", got)
	}
}

func TestLoadHeaderValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("name: foo\nreplicas: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for missing type/cid/aid")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Errorf("error should mention missing type field, got %v", err)
	}
}

func TestIsServiceYAMLFilename(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"runos.service.mycluster3.postgresql.abc12.yaml", true},   // canonical
		{"runos.service.mycluster2.cert-manager.aegrg.yaml", true}, // canonical with hyphenated type
		{"runos.service.zz.x.yaml", true},                   // tolerated old shape (prefix+suffix)
		{"runos.mycluster3.abc12.yaml", false},                     // app yaml, not service yaml
		{"runos.service.yaml", true},                        // boundary; prefix matches, accept
		{"runos.service.mycluster3.postgresql.abc12.yml", false},   // wrong extension (canonical detector)
		{".runos.service.mycluster3.postgresql.abc12.yaml", false}, // hidden file, skip
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsServiceYAMLFilename(c.name); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestFindByID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Canonical-named service yaml.
	canonical := DefaultFilename("mycluster3", "postgresql", "abc12")
	if err := Save(filepath.Join(dir, canonical), &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x"},
	}); err != nil {
		t.Fatal(err)
	}

	// User-renamed service yaml (different name, header still authoritative).
	if err := Save(filepath.Join(dir, "my-renamed-service.yaml"), &ServiceYAML{
		Type: "valkey", ID: "vsid1", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "y"},
	}); err != nil {
		t.Fatal(err)
	}

	// Decoy: app yaml with header that DOESN'T have type:.
	if err := os.WriteFile(filepath.Join(dir, "runos.mycluster3.app.yaml"),
		[]byte("app: someapp\nid: app1\ncid: mycluster3\naid: acct1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hidden file with matching cid+id; must be ignored.
	if err := os.WriteFile(filepath.Join(dir, ".runos.service.mycluster3.postgresql.hidden.yaml"),
		[]byte("type: postgresql\nid: abc12\ncid: mycluster3\naid: acct1\nname: hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name           string
		cid, sid       string
		wantSubstring  string
		wantEmpty      bool
	}{
		{"matches canonical filename", "mycluster3", "abc12", canonical, false},
		{"matches renamed file", "mycluster3", "vsid1", "my-renamed-service.yaml", false},
		{"no match returns empty", "mycluster3", "missing", "", true},
		{"different cid no match", "mycluster2", "abc12", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FindByID(dir, c.cid, c.sid)
			if err != nil {
				t.Fatal(err)
			}
			if c.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, c.wantSubstring) {
				t.Errorf("expected path containing %q, got %q", c.wantSubstring, got)
			}
		})
	}
}

func TestFindByID_DirMissing(t *testing.T) {
	t.Parallel()
	got, err := FindByID(filepath.Join(t.TempDir(), "does-not-exist"), "mycluster3", "abc12")
	if err != nil {
		t.Errorf("missing dir should be a non-error empty result, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty path, got %q", got)
	}
}

// Regression test for V4 (VCS_DEPLOY_TEST_NOTES.md): FindByIDInTree must
// recurse into subdirectories so projects that organise infra as
// `infra/runos/apps/` + `infra/runos/services/` (services committed
// outside the app dir) are recognised. Pre-fix, apps_pull's cascade only
// checked the appDir and so re-wrote duplicate service yamls into
// `apps/` on every pull.
func TestFindByIDInTree_FindsServiceInSiblingDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	servicesDir := filepath.Join(root, "infra", "runos", "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	appsDir := filepath.Join(root, "infra", "runos", "apps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// User-renamed canonical service yaml in services/, header authoritative.
	if err := Save(filepath.Join(servicesDir, "aliens-mysql.mycluster2.mysql.f8jlf.yaml"), &ServiceYAML{
		Type: "mysql", ID: "f8jlf", CID: "mycluster2", AID: "acct1",
		Fields: map[string]any{"name": "aliens-mysql"},
	}); err != nil {
		t.Fatal(err)
	}

	// Searching from any ancestor finds the file in its nested location.
	got, err := FindByIDInTree(root, "mycluster2", "f8jlf")
	if err != nil {
		t.Fatalf("FindByIDInTree: %v", err)
	}
	if !strings.Contains(got, "aliens-mysql.mycluster2.mysql.f8jlf.yaml") {
		t.Errorf("expected path containing the canonical leaf, got %q", got)
	}

	// No match still returns empty.
	got, err = FindByIDInTree(root, "mycluster2", "missing")
	if err != nil {
		t.Fatalf("FindByIDInTree (no-match): %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for no match, got %q", got)
	}
}

// Companion test: heavy / vendored / hidden directories must be skipped
// so a workspace scan doesn't slow to a crawl on real-world repos.
// node_modules / vendor / .git are the canonical offenders.
func TestFindByIDInTree_SkipsHeavyDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, sub := range []string{"node_modules", "vendor", ".git"} {
		nested := filepath.Join(root, sub, "deep")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		// Plant a yaml with the matching cid+id; the scan must not see it.
		if err := Save(filepath.Join(nested, DefaultFilename("mycluster2", "mysql", "f8jlf")), &ServiceYAML{
			Type: "mysql", ID: "f8jlf", CID: "mycluster2", AID: "acct1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindByIDInTree(root, "mycluster2", "f8jlf")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("FindByIDInTree must skip node_modules/vendor/.git; matched anyway at %q", got)
	}
}
