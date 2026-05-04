package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/apps"
)

func TestWriteStateLabel(t *testing.T) {
	tests := []struct {
		name string
		in   apps.WriteResult
		want string
	}{
		{"written", apps.WriteResult{Path: "/x/a.yaml"}, "written"},
		{"in sync", apps.WriteResult{Path: "/x/a.yaml", InSync: true}, "in sync"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := writeStateLabel(tt.in); got != tt.want {
				t.Errorf("writeStateLabel(%+v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWrittenInSyncLabel(t *testing.T) {
	tests := []struct {
		name             string
		written, inSync  int
		want             string
	}{
		{"all in sync", 0, 3, "all 3 in sync"},
		{"all written", 2, 0, "2 written"},
		{"mixed", 1, 2, "1 written, 2 in sync"},
		{"all in sync (single)", 0, 1, "all 1 in sync"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := writtenInSyncLabel(tt.written, tt.inSync); got != tt.want {
				t.Errorf("writtenInSyncLabel(%d, %d) = %q, want %q", tt.written, tt.inSync, got, tt.want)
			}
		})
	}
}

func TestValidatePullPlan(t *testing.T) {
	tests := []struct {
		name      string
		plan      pullPlan
		force     bool
		codeFlag  bool
		wantErr   string // substring; "" means should succeed
	}{
		{"bulk, no force/code", pullPlan{mode: "bulk"}, false, false, ""},
		{"yaml mode + force ok", pullPlan{mode: "yaml", appID: "x"}, true, false, ""},
		{"id-flat + code ok", pullPlan{mode: "id-flat", appID: "x"}, false, true, ""},
		{"bulk + force rejected", pullPlan{mode: "bulk"}, true, false, "--force requires a single-app target"},
		{"bulk + code rejected", pullPlan{mode: "bulk"}, false, true, "--code requires a single-app target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePullPlan(tt.plan, tt.force, tt.codeFlag)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestResolvePullPlan(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "runos.yaml")
	yaml := []byte(`app: greenfingers
deployType: cli
id: appid4
cid: mycluster3
aid: myacct
replicas: 1
servicePortMappings:
    - port: 8080
      standardHttps: true
`)
	if err := os.WriteFile(yamlPath, yaml, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	tests := []struct {
		name        string
		args        []string
		all         bool
		appIDFlag   string
		outFlag     string
		expectedCID string
		expectedAID string
		wantMode    string
		wantAppID   string
		wantDir     string // for single modes; "" to skip check
		wantErr     string
	}{
		{
			name:        "yaml positional → yaml mode anchored on yaml's dir",
			args:        []string{yamlPath},
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "yaml",
			wantAppID:   "appid4",
			wantDir:     dir,
		},
		{
			name:        "yaml positional + --out → --out wins",
			args:        []string{yamlPath},
			outFlag:     "/tmp/somewhere",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "yaml",
			wantAppID:   "appid4",
			wantDir:     "/tmp/somewhere",
		},
		{
			name:        "--all → bulk",
			all:         true,
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "bulk",
			wantAppID:   "",
		},
		{
			name:        "--all + positional → error",
			all:         true,
			args:        []string{yamlPath},
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantErr:     "mutually exclusive",
		},
		{
			name:        "--all + --app-id → error",
			all:         true,
			appIDFlag:   "ab12c",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantErr:     "mutually exclusive",
		},
		{
			name:        "--app-id alone → id-subdir",
			appIDFlag:   "ab12c",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "id-subdir",
			wantAppID:   "ab12c",
		},
		{
			name:        "--app-id + --out → id-flat",
			appIDFlag:   "ab12c",
			outFlag:     "/tmp/checkout",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "id-flat",
			wantAppID:   "ab12c",
			wantDir:     "/tmp/checkout",
		},
		{
			name:        "yaml + matching --app-id → ok",
			args:        []string{yamlPath},
			appIDFlag:   "appid4",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantMode:    "yaml",
			wantAppID:   "appid4",
		},
		{
			name:        "yaml + mismatched --app-id → error",
			args:        []string{yamlPath},
			appIDFlag:   "wrong",
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantErr:     "doesn't match the yaml",
		},
		{
			name:        "yaml on wrong cluster → error",
			args:        []string{yamlPath},
			expectedCID: "k1",
			expectedAID: "myacct",
			wantErr:     "cluster mismatch",
		},
		{
			name:        "yaml on wrong account → error",
			args:        []string{yamlPath},
			expectedCID: "mycluster3",
			expectedAID: "wrong-account",
			wantErr:     "logged in as",
		},
		{
			name:        "missing yaml → error",
			args:        []string{filepath.Join(dir, "no-such-file.yaml")},
			expectedCID: "mycluster3",
			expectedAID: "myacct",
			wantErr:     "not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePullPlan(tt.args, tt.all, tt.appIDFlag, tt.outFlag, tt.expectedCID, tt.expectedAID)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", got.mode, tt.wantMode)
			}
			if got.appID != tt.wantAppID {
				t.Errorf("appID = %q, want %q", got.appID, tt.wantAppID)
			}
			if tt.wantDir != "" && got.fixedDir != tt.wantDir {
				t.Errorf("fixedDir = %q, want %q", got.fixedDir, tt.wantDir)
			}
		})
	}
}

func TestResolvePullPlan_AutoDetectFromCwd(t *testing.T) {
	dir := t.TempDir()
	// Single valid candidate.
	yamlPath := filepath.Join(dir, "runos.yaml")
	if err := os.WriteFile(yamlPath, []byte(`app: x
deployType: cli
id: appid5
cid: mycluster3
aid: myacct
replicas: 1
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Drop a non-yaml that contains "runos", should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "runos-notes.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	t.Chdir(dir)

	// Use the cwd that resolvePullPlan will see (macOS resolves /var to
	// /private/var via symlink, so dir from t.TempDir doesn't match the
	// chdir'd cwd byte-for-byte).
	resolvedCwd, _ := os.Getwd()

	plan, err := resolvePullPlan(nil, false, "", "", "mycluster3", "myacct")
	if err != nil {
		t.Fatalf("auto-detect with single candidate failed: %v", err)
	}
	if plan.mode != "yaml" || plan.appID != "appid5" {
		t.Errorf("auto-detect yielded %+v", plan)
	}
	if plan.fixedDir != resolvedCwd {
		t.Errorf("fixedDir should be cwd (%s), got %s", resolvedCwd, plan.fixedDir)
	}
}

func TestResolvePullPlan_AutoDetectAmbiguous(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"runos.yaml", "runos.prod.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`app: x
deployType: cli
id: aaaaa
cid: mycluster3
aid: myacct
replicas: 1
`), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	t.Chdir(dir)

	_, err := resolvePullPlan(nil, false, "", "", "mycluster3", "myacct")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "multiple yaml candidates") {
		t.Errorf("error should mention multiple candidates, got %q", err.Error())
	}
}

func TestResolvePullPlan_AutoDetectNoCandidates(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := resolvePullPlan(nil, false, "", "", "mycluster3", "myacct")
	if err == nil {
		t.Fatal("expected error for no candidates")
	}
	if !strings.Contains(err.Error(), "no runos") {
		t.Errorf("error should mention no runos*.yaml, got %q", err.Error())
	}
}

func TestPullPlan_AppDirFor(t *testing.T) {
	bulk := pullPlan{mode: "bulk", bulkParent: "/parent"}
	if got := bulk.appDirFor("mycluster3", "appid4"); got != "/parent/runos.mycluster3.appid4" {
		t.Errorf("bulk: %s", got)
	}

	yamlMode := pullPlan{mode: "yaml", fixedDir: "/some/app"}
	if got := yamlMode.appDirFor("mycluster3", "appid4"); got != "/some/app" {
		t.Errorf("yaml: %s", got)
	}

	idFlat := pullPlan{mode: "id-flat", fixedDir: "/checkout"}
	if got := idFlat.appDirFor("mycluster3", "appid4"); got != "/checkout" {
		t.Errorf("id-flat: %s", got)
	}
}

// TestPullPlan_DefaultSourceDir maps modes to the sourceDir auto-set
// applied to freshly-pulled yamls. Subdir modes ("bulk", "id-subdir")
// recommend the directory-per-app shape with source one level up;
// flat modes leave sourceDir empty so the user keeps explicit control.
func TestPullPlan_DefaultSourceDir(t *testing.T) {
	cases := []struct {
		mode string
		want string
	}{
		{"bulk", ".."},
		{"id-subdir", ".."},
		{"id-flat", ""},
		{"yaml", ""},
		{"unknown-mode", ""},
	}
	for _, c := range cases {
		got := pullPlan{mode: c.mode}.defaultSourceDir()
		if got != c.want {
			t.Errorf("mode=%q: got %q, want %q", c.mode, got, c.want)
		}
	}
}

// TestPickSourceDir covers preservation vs default-fill. Re-pulls must
// not clobber a manually-set sourceDir; fresh pulls take the caller's
// default.
func TestPickSourceDir(t *testing.T) {
	t.Run("no local yaml falls through to default", func(t *testing.T) {
		dir := t.TempDir()
		got := pickSourceDir(filepath.Join(dir, "runos.yaml"), "..")
		if got != ".." {
			t.Errorf("got %q, want %q", got, "..")
		}
	})

	t.Run("default empty stays empty when no local yaml", func(t *testing.T) {
		dir := t.TempDir()
		got := pickSourceDir(filepath.Join(dir, "runos.yaml"), "")
		if got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})

	t.Run("existing local sourceDir wins over default", func(t *testing.T) {
		// User pulled, manually edited sourceDir to ../shared, then
		// re-pulls. The edit must survive.
		dir := t.TempDir()
		body := `app: web
deployType: cli
id: appid4
cid: mycluster3
aid: myacct
sourceDir: ../shared
`
		yamlPath := filepath.Join(dir, "runos.yaml")
		if err := os.WriteFile(yamlPath, []byte(body), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got := pickSourceDir(yamlPath, "..")
		if got != "../shared" {
			t.Errorf("re-pull must preserve existing sourceDir; got %q, want %q", got, "../shared")
		}
	})

	t.Run("empty existing sourceDir falls through to default", func(t *testing.T) {
		// Local yaml exists but doesn't pin sourceDir. Default fills it.
		dir := t.TempDir()
		body := `app: web
deployType: cli
id: appid4
cid: mycluster3
aid: myacct
`
		yamlPath := filepath.Join(dir, "runos.yaml")
		if err := os.WriteFile(yamlPath, []byte(body), 0644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got := pickSourceDir(yamlPath, "..")
		if got != ".." {
			t.Errorf("got %q, want %q (empty existing should accept default)", got, "..")
		}
	})
}

func TestPulledAppEntry_AllInSync(t *testing.T) {
	tests := []struct {
		name string
		in   pulledAppEntry
		want bool
	}{
		{
			name: "yaml + env in sync, no secrets/overrides",
			in: pulledAppEntry{
				YAML: apps.WriteResult{InSync: true},
				Env:  &apps.WriteResult{InSync: true},
			},
			want: true,
		},
		{
			name: "yaml in sync, no env, no secrets/overrides",
			in: pulledAppEntry{
				YAML: apps.WriteResult{InSync: true},
			},
			want: true,
		},
		{
			name: "yaml drifted",
			in: pulledAppEntry{
				YAML: apps.WriteResult{InSync: false},
			},
			want: false,
		},
		{
			name: "env drifted",
			in: pulledAppEntry{
				YAML: apps.WriteResult{InSync: true},
				Env:  &apps.WriteResult{InSync: false},
			},
			want: false,
		},
		{
			name: "secret file written",
			in: pulledAppEntry{
				YAML:               apps.WriteResult{InSync: true},
				SecretFilesWritten: 1,
			},
			want: false,
		},
		{
			name: "override written",
			in: pulledAppEntry{
				YAML:             apps.WriteResult{InSync: true},
				OverridesWritten: 1,
			},
			want: false,
		},
		{
			name: "secrets present but all in sync",
			in: pulledAppEntry{
				YAML:               apps.WriteResult{InSync: true},
				SecretFilesTotal:   3,
				SecretFilesWritten: 0,
			},
			want: true,
		},
		{
			// Regression: pull --code re-extracts source files and updates
			// the sidecar; the summary should NOT report "in sync".
			name: "code re-extracted (yaml + env in sync, but Code populated)",
			in: pulledAppEntry{
				YAML: apps.WriteResult{InSync: true},
				Env:  &apps.WriteResult{InSync: true},
				Code: &pulledCodeEntry{CliUploadID: "x", FilesWritten: 4},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.allInSync(); got != tt.want {
				t.Errorf("allInSync() = %v, want %v", got, tt.want)
			}
		})
	}
}

