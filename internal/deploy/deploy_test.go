package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// DeployConfig field-order alignment with apps.PulledApp
// ---------------------------------------------------------------------------

// TestDeployConfig_AppSpecBlockOrderMatchesPulledApp asserts the order
// in which DeployConfig marshals its AppSpec fields. The expectation
// mirrors apps.PulledApp's marshal order so a yaml written by deploy
// diffs byte-clean against a server-rendered PulledApp. We do this by
// inspecting the marshaled output rather than struct reflection so the
// guarantee survives field renames as long as the tags stay aligned.
func TestDeployConfig_AppSpecBlockOrderMatchesPulledApp(t *testing.T) {
	replicas := 3
	cfg := &DeployConfig{
		App:                        "web",
		DeployType:                 "cli",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		Env:                        ".env",
		Replicas:                   &replicas,
		ClusterDomainID:            "elpfn",
		ResourceRequirementClassID: "app.sl1.beff",
		ServicePortMappings: []ServicePortMapping{
			{Port: 3000},
		},
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// Required ordering: each successor must appear AFTER its predecessor.
	expectedOrder := []string{
		"app:",
		"deployType:",
		"id:",
		"cid:",
		"aid:",
		"env:",
		"replicas:",
		"clusterDomainId:",
		"resourceRequirementClassId:",
		"servicePortMappings:",
	}
	prev := -1
	for _, key := range expectedOrder {
		idx := indexOf(got, key)
		if idx < 0 {
			t.Errorf("expected key %q in marshaled yaml, got:\n%s", key, got)
			continue
		}
		if idx <= prev {
			t.Errorf("key %q appears before an earlier key (idx %d, prev %d). Output:\n%s", key, idx, prev, got)
		}
		prev = idx
	}
}

// indexOf returns the byte offset of needle in haystack, or -1.
func indexOf(haystack, needle string) int {
	return bytes.Index([]byte(haystack), []byte(needle))
}

// TestDeployConfig_SourceDirRoundTrip pins yaml round-tripping for the
// directory-per-app field. Empty stays omitted, set stays set.
func TestDeployConfig_SourceDirRoundTrip(t *testing.T) {
	t.Run("set sourceDir round-trips", func(t *testing.T) {
		cfg := &DeployConfig{App: "web", SourceDir: ".."}
		out, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(out), "sourceDir: ..") {
			t.Errorf("marshaled yaml missing sourceDir: ..\n%s", out)
		}
		var got DeployConfig
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.SourceDir != ".." {
			t.Errorf("round-tripped SourceDir = %q, want %q", got.SourceDir, "..")
		}
	})

	t.Run("empty sourceDir is omitted from output", func(t *testing.T) {
		cfg := &DeployConfig{App: "web"}
		out, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), "sourceDir:") {
			t.Errorf("empty SourceDir must be omitted; got:\n%s", out)
		}
	})

	t.Run("sourceDir is not sent in JSON to conductor", func(t *testing.T) {
		// json:"-" keeps it CLI-side. Conductor doesn't know or care.
		cfg := &DeployConfig{App: "web", SourceDir: ".."}
		out, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), "sourceDir") {
			t.Errorf("sourceDir must not appear in JSON payload; got %s", out)
		}
	})
}

// TestDeployConfig_NilPointerFieldsRoundTripCleanly guards against the
// Replicas/HealthCheckPort/MetricsPort/StorageMb pointer fields leaking
// into the marshaled yaml as zero values when the user never set them.
// A spurious "replicas: 0" or "metricsPort: 0" would cause a fresh
// DeployConfig-written yaml to drift against the server's PulledApp
// projection (which only emits these when the server returns non-zero).
func TestDeployConfig_NilPointerFieldsRoundTripCleanly(t *testing.T) {
	cfg := &DeployConfig{
		App:  "web",
		Port: 3000,
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, key := range []string{"replicas:", "healthCheckPort:", "metricsPort:", "storageMb:"} {
		if strings.Contains(got, key) {
			t.Errorf("yaml contains %q despite nil pointer; output:\n%s", key, got)
		}
	}

	// And that an explicit zero, when callers really want one, IS preserved.
	zero := 0
	cfg2 := &DeployConfig{App: "web", Port: 3000, Replicas: &zero}
	out2, err := yaml.Marshal(cfg2)
	if err != nil {
		t.Fatalf("marshal explicit zero: %v", err)
	}
	if !strings.Contains(string(out2), "replicas: 0") {
		t.Errorf("explicit replicas=0 should marshal to 'replicas: 0', got:\n%s", out2)
	}

	// Round-trip: unmarshaling yaml without these keys should leave the
	// pointers nil (not point to zero).
	var back DeployConfig
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Replicas != nil {
		t.Errorf("Replicas should remain nil after round-trip, got %v", *back.Replicas)
	}
	if back.HealthCheckPort != nil {
		t.Errorf("HealthCheckPort should remain nil after round-trip, got %v", *back.HealthCheckPort)
	}
	if back.MetricsPort != nil {
		t.Errorf("MetricsPort should remain nil after round-trip, got %v", *back.MetricsPort)
	}
	if back.StorageMb != nil {
		t.Errorf("StorageMb should remain nil after round-trip, got %v", *back.StorageMb)
	}
}

// ---------------------------------------------------------------------------
// HasLegacyFields — gate uses this to emit migration-tailored output
// ---------------------------------------------------------------------------

func TestHasLegacyFields(t *testing.T) {
	stdHttps := true
	tests := []struct {
		name string
		in   *DeployConfig
		want bool
	}{
		{
			name: "nil",
			in:   nil,
			want: false,
		},
		{
			name: "fully canonical (no legacy)",
			in: &DeployConfig{
				App: "x",
				ServicePortMappings: []ServicePortMapping{
					{Port: 8080},
				},
			},
			want: false,
		},
		{
			name: "legacy port at top level",
			in:   &DeployConfig{App: "x", Port: 8080},
			want: true,
		},
		{
			name: "legacy domain at top level",
			in:   &DeployConfig{App: "x", Domain: StringOrSlice{"example.com"}},
			want: true,
		},
		{
			name: "legacy standardHttps at top level",
			in:   &DeployConfig{App: "x", StandardHttps: &stdHttps},
			want: true,
		},
		{
			name: "mixed legacy + canonical also flagged",
			in: &DeployConfig{
				App:  "x",
				Port: 8080,
				ServicePortMappings: []ServicePortMapping{
					{Port: 8080},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasLegacyFields(tt.in); got != tt.want {
				t.Errorf("HasLegacyFields(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DeployConfig.Validate()
// ---------------------------------------------------------------------------

func TestDeployConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  DeployConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid config",
			config:  DeployConfig{App: "myapp", Port: 8080},
			wantErr: false,
		},
		{
			name:    "valid config with port 1",
			config:  DeployConfig{App: "myapp", Port: 1},
			wantErr: false,
		},
		{
			name:    "valid config with port 65535",
			config:  DeployConfig{App: "myapp", Port: 65535},
			wantErr: false,
		},
		{
			name:    "missing app name",
			config:  DeployConfig{App: "", Port: 8080},
			wantErr: true,
			errMsg:  "app name is required",
		},
		{
			name:    "missing port (zero value)",
			config:  DeployConfig{App: "myapp", Port: 0},
			wantErr: true,
			errMsg:  "valid port",
		},
		{
			name:    "negative port",
			config:  DeployConfig{App: "myapp", Port: -1},
			wantErr: true,
			errMsg:  "valid port",
		},
		{
			name:    "port exceeds 65535",
			config:  DeployConfig{App: "myapp", Port: 65536},
			wantErr: true,
			errMsg:  "valid port",
		},
		{
			name:    "port way above range",
			config:  DeployConfig{App: "myapp", Port: 100000},
			wantErr: true,
			errMsg:  "valid port",
		},
		{
			name:    "both missing",
			config:  DeployConfig{},
			wantErr: true,
			errMsg:  "app name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ValidateAID()
// ---------------------------------------------------------------------------

func TestValidateAID(t *testing.T) {
	tests := []struct {
		name       string
		configAID  string
		sessionAID string
		wantErr    bool
		errMsg     string
	}{
		{
			name:       "matching AIDs",
			configAID:  "account-123",
			sessionAID: "account-123",
			wantErr:    false,
		},
		{
			name:       "mismatching AIDs",
			configAID:  "account-123",
			sessionAID: "account-456",
			wantErr:    true,
			errMsg:     "does not match session AID",
		},
		{
			name:       "empty config AID allows any session",
			configAID:  "",
			sessionAID: "account-789",
			wantErr:    false,
		},
		{
			name:       "both empty",
			configAID:  "",
			sessionAID: "",
			wantErr:    false,
		},
		{
			name:       "config AID set but session empty",
			configAID:  "account-123",
			sessionAID: "",
			wantErr:    true,
			errMsg:     "does not match session AID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAID(tt.configAID, tt.sessionAID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if tt.errMsg != "" && !contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoadEnvFile()
// ---------------------------------------------------------------------------

func TestLoadEnvFile(t *testing.T) {
	t.Run("valid env file with various formats", func(t *testing.T) {
		dir := t.TempDir()
		content := `# This is a comment
DB_HOST=localhost
DB_PORT=5432

DB_NAME="mydb"
DB_PASS='secret'
MULTI_EQUALS=key=value=extra
  SPACED_KEY = spaced_value
`
		path := filepath.Join(dir, ".runos.test-cluster.env")
		writeFile(t, path, content)

		envVars, err := LoadEnvFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := map[string]string{
			"DB_HOST":      "localhost",
			"DB_PORT":      "5432",
			"DB_NAME":      "mydb",
			"DB_PASS":      "secret",
			"MULTI_EQUALS": "key=value=extra",
			"SPACED_KEY":   "spaced_value",
		}

		for k, want := range expected {
			got, ok := envVars[k]
			if !ok {
				t.Errorf("missing key %q", k)
				continue
			}
			if got != want {
				t.Errorf("key %q: got %q, want %q", k, got, want)
			}
		}
	})

	t.Run("missing file returns nil", func(t *testing.T) {
		envVars, err := LoadEnvFile("/nonexistent/path/.env")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars != nil {
			t.Fatalf("expected nil, got %v", envVars)
		}
	})

	t.Run("empty path returns nil", func(t *testing.T) {
		envVars, err := LoadEnvFile("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars != nil {
			t.Fatalf("expected nil, got %v", envVars)
		}
	})

	t.Run("comments and empty lines are skipped", func(t *testing.T) {
		dir := t.TempDir()
		content := `# comment 1
# comment 2

KEY=value

# another comment
`
		path := filepath.Join(dir, "my.env")
		writeFile(t, path, content)

		envVars, err := LoadEnvFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(envVars) != 1 {
			t.Fatalf("expected 1 key, got %d: %v", len(envVars), envVars)
		}
		if envVars["KEY"] != "value" {
			t.Errorf("expected KEY=value, got KEY=%s", envVars["KEY"])
		}
	})

	t.Run("double-quoted values have quotes stripped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		writeFile(t, path, `VAL="hello world"`)

		envVars, err := LoadEnvFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars["VAL"] != "hello world" {
			t.Errorf("got %q, want %q", envVars["VAL"], "hello world")
		}
	})

	t.Run("single-quoted values have quotes stripped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.env")
		writeFile(t, path, `VAL='hello world'`)

		envVars, err := LoadEnvFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars["VAL"] != "hello world" {
			t.Errorf("got %q, want %q", envVars["VAL"], "hello world")
		}
	})
}

// ---------------------------------------------------------------------------
// ResolveEnvFiles()
// ---------------------------------------------------------------------------

func TestResolveEnvFiles(t *testing.T) {
	t.Run("explicit secretEnv and env in config are used as-is", func(t *testing.T) {
		dir := t.TempDir()
		config := &DeployConfig{App: "myapp", Port: 8080, SecretEnv: "custom-secret.env", Env: "custom.env"}

		paths, changed := ResolveEnvFiles(dir, config, "cid1")
		if changed {
			t.Fatal("expected changed=false when both fields are already set")
		}
		if paths.Secret != filepath.Join(dir, "custom-secret.env") {
			t.Errorf("Secret: got %q, want %q", paths.Secret, filepath.Join(dir, "custom-secret.env"))
		}
		if paths.Plain != filepath.Join(dir, "custom.env") {
			t.Errorf("Plain: got %q, want %q", paths.Plain, filepath.Join(dir, "custom.env"))
		}
	})

	t.Run("default uses CID and ID for both files", func(t *testing.T) {
		dir := t.TempDir()
		config := &DeployConfig{App: "myapp", Port: 8080, ID: "app123"}

		paths, changed := ResolveEnvFiles(dir, config, "cid1")
		if !changed {
			t.Fatal("expected changed=true for default paths")
		}
		if config.SecretEnv != ".runos.cid1.app123.env" {
			t.Errorf("config.SecretEnv = %q, want %q", config.SecretEnv, ".runos.cid1.app123.env")
		}
		if config.Env != "runos.cid1.app123.config.env" {
			t.Errorf("config.Env = %q, want %q", config.Env, "runos.cid1.app123.config.env")
		}
		if paths.Secret != filepath.Join(dir, ".runos.cid1.app123.env") {
			t.Errorf("Secret path: got %q", paths.Secret)
		}
		if paths.Plain != filepath.Join(dir, "runos.cid1.app123.config.env") {
			t.Errorf("Plain path: got %q", paths.Plain)
		}
	})

	t.Run("no ID returns empty paths", func(t *testing.T) {
		dir := t.TempDir()
		config := &DeployConfig{App: "myapp", Port: 8080}

		paths, changed := ResolveEnvFiles(dir, config, "cid1")
		if changed {
			t.Fatal("expected changed=false when no ID")
		}
		if paths.Secret != "" || paths.Plain != "" {
			t.Errorf("expected empty paths, got %+v", paths)
		}
	})
}

// ---------------------------------------------------------------------------
// ResolveArchiveRoot()
// ---------------------------------------------------------------------------

func TestResolveArchiveRoot(t *testing.T) {
	t.Run("empty sourceDir resolves to configDir", func(t *testing.T) {
		dir := t.TempDir()
		got, err := ResolveArchiveRoot(dir, "")
		if err != nil {
			t.Fatalf("ResolveArchiveRoot: %v", err)
		}
		if got != filepath.Clean(dir) {
			t.Errorf("got %q, want %q", got, filepath.Clean(dir))
		}
	})

	t.Run(`"." sourceDir resolves to configDir`, func(t *testing.T) {
		dir := t.TempDir()
		got, err := ResolveArchiveRoot(dir, ".")
		if err != nil {
			t.Fatalf("ResolveArchiveRoot: %v", err)
		}
		if got != filepath.Clean(dir) {
			t.Errorf("got %q, want %q", got, filepath.Clean(dir))
		}
	})

	t.Run(`".." resolves to parent`, func(t *testing.T) {
		// Directory-per-app shape: yaml in <project>/runos.<cid>.<id>/,
		// source at <project>. sourceDir: ".." must resolve to <project>.
		parent := t.TempDir()
		appDir := filepath.Join(parent, "runos.mycluster3.appid4")
		mkdirAll(t, appDir)

		got, err := ResolveArchiveRoot(appDir, "..")
		if err != nil {
			t.Fatalf("ResolveArchiveRoot: %v", err)
		}
		if got != filepath.Clean(parent) {
			t.Errorf("got %q, want %q", got, filepath.Clean(parent))
		}
	})

	t.Run("absolute sourceDir is rejected", func(t *testing.T) {
		// Yaml stays portable: an absolute path locks to one machine.
		dir := t.TempDir()
		_, err := ResolveArchiveRoot(dir, "/etc")
		if err == nil {
			t.Fatal("expected error for absolute sourceDir")
		}
		if !strings.Contains(err.Error(), "relative") {
			t.Errorf("error should explain why absolute is rejected; got %q", err.Error())
		}
	})

	t.Run("non-existent sourceDir is rejected with clear error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := ResolveArchiveRoot(dir, "does-not-exist")
		if err == nil {
			t.Fatal("expected error for missing sourceDir")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("error should mention 'does not exist'; got %q", err.Error())
		}
	})

	t.Run("file (not directory) sourceDir is rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "afile"), "x\n")
		_, err := ResolveArchiveRoot(dir, "afile")
		if err == nil {
			t.Fatal("expected error for file sourceDir")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("error should mention not-a-directory; got %q", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// WarnLegacyEnv()
// ---------------------------------------------------------------------------

func TestWarnLegacyEnv(t *testing.T) {
	t.Run("silent when secretEnv: explicitly set", func(t *testing.T) {
		// User has migrated to the explicit form; nothing to warn about
		// even if the old file is still on disk. (The legacy form was
		// always for the secret env file, never the new plain config
		// file.)
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".runos.abc.env"), "X=1\n")
		config := &DeployConfig{App: "a", Port: 1, SecretEnv: "elsewhere.env"}

		stderr := captureStderr(t, func() {
			WarnLegacyEnv(dir, config, "abc")
		})
		if stderr != "" {
			t.Errorf("expected no warning when secretEnv: is set, got %q", stderr)
		}
	})

	t.Run("silent when no legacy file present", func(t *testing.T) {
		// Fresh project, nothing to migrate.
		dir := t.TempDir()
		config := &DeployConfig{App: "a", Port: 1}

		stderr := captureStderr(t, func() {
			WarnLegacyEnv(dir, config, "abc")
		})
		if stderr != "" {
			t.Errorf("expected no warning when no legacy file, got %q", stderr)
		}
	})

	t.Run("warns when legacy file exists and env unset", func(t *testing.T) {
		// The case multi-yaml broke. User must see the rename hint
		// because their env vars stopped being loaded silently.
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".runos.abc.env"), "X=1\n")
		config := &DeployConfig{App: "a", Port: 1, ID: "appid4"}

		stderr := captureStderr(t, func() {
			WarnLegacyEnv(dir, config, "abc")
		})
		if !strings.Contains(stderr, ".runos.abc.env") {
			t.Errorf("warning should reference legacy filename; got %q", stderr)
		}
		if !strings.Contains(stderr, ".runos.abc.appid4.env") {
			t.Errorf("warning should suggest the per-app filename; got %q", stderr)
		}
	})
}

// ---------------------------------------------------------------------------
// isHidden()
// ---------------------------------------------------------------------------

func TestIsHidden(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		expect bool
	}{
		{name: "hidden file", path: ".gitignore", expect: true},
		{name: "hidden directory", path: ".git", expect: true},
		{name: "file in hidden dir", path: ".git/config", expect: true},
		{name: "nested hidden dir", path: "src/.hidden/file.go", expect: true},
		{name: "deeply nested hidden", path: "a/b/.c/d/e.txt", expect: true},
		{name: "normal file", path: "main.go", expect: false},
		{name: "normal nested file", path: "src/app/main.go", expect: false},
		{name: "file starting with dot in name only", path: "src/app/.env", expect: true},
		{name: "non-hidden file with dot", path: "src/app/file.test.go", expect: false},
		{name: "Dockerfile", path: "Dockerfile", expect: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHidden(tt.path)
			if got != tt.expect {
				t.Errorf("isHidden(%q) = %v, want %v", tt.path, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// shouldIgnore()
// ---------------------------------------------------------------------------

func TestShouldIgnore(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		isDir    bool
		patterns []string
		expect   bool
	}{
		{
			name:     "no patterns",
			path:     "main.go",
			patterns: nil,
			expect:   false,
		},
		{
			name:     "exact file match",
			path:     "README.md",
			isDir:    false,
			patterns: []string{"README.md"},
			expect:   true,
		},
		{
			name:     "wildcard pattern",
			path:     "output.log",
			isDir:    false,
			patterns: []string{"*.log"},
			expect:   true,
		},
		{
			name:     "directory-only pattern matches dir",
			path:     "build",
			isDir:    true,
			patterns: []string{"build/"},
			expect:   true,
		},
		{
			name:     "directory-only pattern does not match file",
			path:     "build",
			isDir:    false,
			patterns: []string{"build/"},
			expect:   false,
		},
		{
			name:     "Dockerfile is always included",
			path:     "Dockerfile",
			isDir:    false,
			patterns: []string{"Dockerfile"},
			expect:   false,
		},
		{
			name:     "dockerfile lowercase is always included",
			path:     "dockerfile",
			isDir:    false,
			patterns: []string{"dockerfile"},
			expect:   false,
		},
		{
			name:     ".dockerignore is always included",
			path:     ".dockerignore",
			isDir:    false,
			patterns: []string{".dockerignore", ".*"},
			expect:   false,
		},
		{
			name:     "pattern matching basename",
			path:     "src/temp.log",
			isDir:    false,
			patterns: []string{"*.log"},
			expect:   true,
		},
		{
			name:     "file inside ignored directory prefix",
			path:     "node_modules/express/index.js",
			isDir:    false,
			patterns: []string{"node_modules"},
			expect:   true,
		},
		{
			name:     "doublestar pattern",
			path:     "foo/bar/node_modules",
			isDir:    true,
			patterns: []string{"**/node_modules"},
			expect:   true,
		},
		{
			name:     "no match returns false",
			path:     "src/main.go",
			isDir:    false,
			patterns: []string{"*.log", "build/", "dist"},
			expect:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldIgnore(tt.path, tt.isDir, tt.patterns)
			if got != tt.expect {
				t.Errorf("shouldIgnore(%q, isDir=%v, %v) = %v, want %v",
					tt.path, tt.isDir, tt.patterns, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// matchDoublestar()
// ---------------------------------------------------------------------------

func TestMatchDoublestar(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		expect  bool
	}{
		// **/node_modules, matches at any depth
		{
			name:    "root level match",
			pattern: "**/node_modules",
			path:    "node_modules",
			expect:  true,
		},
		{
			name:    "one level deep",
			pattern: "**/node_modules",
			path:    "foo/node_modules",
			expect:  true,
		},
		{
			name:    "two levels deep",
			pattern: "**/node_modules",
			path:    "foo/bar/node_modules",
			expect:  true,
		},
		{
			name:    "three levels deep",
			pattern: "**/node_modules",
			path:    "a/b/c/node_modules",
			expect:  true,
		},
		{
			name:    "no match similar name",
			pattern: "**/node_modules",
			path:    "node_modules_extra",
			expect:  false,
		},

		// **/*.log, match files with extension at any depth
		{
			name:    "wildcard suffix at root",
			pattern: "**/*.log",
			path:    "output.log",
			expect:  true,
		},
		{
			name:    "wildcard suffix nested",
			pattern: "**/*.log",
			path:    "logs/app/error.log",
			expect:  true,
		},
		{
			name:    "wildcard suffix no match",
			pattern: "**/*.log",
			path:    "logs/app/error.txt",
			expect:  false,
		},

		// prefix/**/suffix, match with prefix directory
		{
			name:    "prefix doublestar suffix direct child",
			pattern: "src/**/test.go",
			path:    "src/test.go",
			expect:  true,
		},
		{
			name:    "prefix doublestar suffix nested",
			pattern: "src/**/test.go",
			path:    "src/pkg/test.go",
			expect:  true,
		},
		{
			name:    "prefix doublestar suffix deeply nested",
			pattern: "src/**/test.go",
			path:    "src/a/b/c/test.go",
			expect:  true,
		},
		{
			name:    "prefix doublestar suffix no prefix match",
			pattern: "src/**/test.go",
			path:    "lib/test.go",
			expect:  false,
		},

		// prefix/**, match everything under prefix
		{
			name:    "prefix doublestar matches child",
			pattern: "vendor/**",
			path:    "vendor/lib/pkg.go",
			expect:  true,
		},
		{
			name:    "prefix doublestar matches direct child",
			pattern: "vendor/**",
			path:    "vendor/file.go",
			expect:  true,
		},
		{
			name:    "prefix doublestar does not match outside",
			pattern: "vendor/**",
			path:    "src/file.go",
			expect:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchDoublestar(tt.pattern, tt.path)
			if got != tt.expect {
				t.Errorf("matchDoublestar(%q, %q) = %v, want %v",
					tt.pattern, tt.path, got, tt.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// StringOrSlice YAML/JSON marshal/unmarshal
// ---------------------------------------------------------------------------

func TestStringOrSlice_UnmarshalYAML(t *testing.T) {
	t.Run("single string value", func(t *testing.T) {
		input := `domain: example.com`
		var result struct {
			Domain StringOrSlice `yaml:"domain"`
		}
		if err := yaml.Unmarshal([]byte(input), &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(result.Domain) != 1 || result.Domain[0] != "example.com" {
			t.Fatalf("expected [example.com], got %v", result.Domain)
		}
	})

	t.Run("array of strings", func(t *testing.T) {
		input := `domain:
  - example.com
  - www.example.com
`
		var result struct {
			Domain StringOrSlice `yaml:"domain"`
		}
		if err := yaml.Unmarshal([]byte(input), &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(result.Domain) != 2 {
			t.Fatalf("expected 2 items, got %d", len(result.Domain))
		}
		if result.Domain[0] != "example.com" || result.Domain[1] != "www.example.com" {
			t.Fatalf("unexpected values: %v", result.Domain)
		}
	})

	t.Run("empty array", func(t *testing.T) {
		input := `domain: []`
		var result struct {
			Domain StringOrSlice `yaml:"domain"`
		}
		if err := yaml.Unmarshal([]byte(input), &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(result.Domain) != 0 {
			t.Fatalf("expected empty slice, got %v", result.Domain)
		}
	})
}

func TestStringOrSlice_MarshalYAML(t *testing.T) {
	t.Run("single element marshals as scalar", func(t *testing.T) {
		data := struct {
			Domain StringOrSlice `yaml:"domain"`
		}{
			Domain: StringOrSlice{"example.com"},
		}
		out, err := yaml.Marshal(&data)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		// Single-element should produce a scalar string, not a list
		if contains(string(out), "- ") {
			t.Fatalf("single element should marshal as scalar, got:\n%s", string(out))
		}
		if !contains(string(out), "example.com") {
			t.Fatalf("expected 'example.com' in output, got:\n%s", string(out))
		}
	})

	t.Run("multiple elements marshal as list", func(t *testing.T) {
		data := struct {
			Domain StringOrSlice `yaml:"domain"`
		}{
			Domain: StringOrSlice{"a.com", "b.com"},
		}
		out, err := yaml.Marshal(&data)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		if !contains(string(out), "- a.com") || !contains(string(out), "- b.com") {
			t.Fatalf("expected YAML list, got:\n%s", string(out))
		}
	})
}

func TestStringOrSlice_MarshalJSON(t *testing.T) {
	t.Run("single element marshals as array", func(t *testing.T) {
		s := StringOrSlice{"only"}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var result []string
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if len(result) != 1 || result[0] != "only" {
			t.Fatalf("expected [\"only\"], got %v", result)
		}
	})

	t.Run("multiple elements marshal as array", func(t *testing.T) {
		s := StringOrSlice{"a.com", "b.com", "c.com"}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		expected := `["a.com","b.com","c.com"]`
		if string(data) != expected {
			t.Fatalf("got %s, want %s", string(data), expected)
		}
	})

	t.Run("empty marshals as empty array", func(t *testing.T) {
		s := StringOrSlice{}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		if string(data) != "[]" {
			t.Fatalf("got %s, want []", string(data))
		}
	})
}

// ---------------------------------------------------------------------------
// CreateTarball()
// ---------------------------------------------------------------------------

func TestCreateTarball(t *testing.T) {
	t.Run("basic tarball creation includes expected files", func(t *testing.T) {
		dir := t.TempDir()

		// Create a project structure
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
		writeFile(t, filepath.Join(dir, "go.mod"), "module test\n")
		mkdirAll(t, filepath.Join(dir, "src"))
		writeFile(t, filepath.Join(dir, "src", "app.go"), "package src\n")
		writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM golang\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		sort.Strings(files)

		expected := []string{"Dockerfile", "go.mod", "main.go", "src", "src/app.go"}
		sort.Strings(expected)

		if len(files) != len(expected) {
			t.Fatalf("expected %d files, got %d: %v", len(expected), len(files), files)
		}
		for i, name := range expected {
			if files[i] != name {
				t.Errorf("file[%d]: got %q, want %q", i, files[i], name)
			}
		}
	})

	t.Run("hidden files and directories are excluded", func(t *testing.T) {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "visible.txt"), "hello\n")
		writeFile(t, filepath.Join(dir, ".hidden"), "secret\n")
		mkdirAll(t, filepath.Join(dir, ".git"))
		writeFile(t, filepath.Join(dir, ".git", "config"), "gitconfig\n")
		writeFile(t, filepath.Join(dir, ".env"), "SECRET=x\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		for _, f := range files {
			if isHidden(f) {
				t.Errorf("hidden file %q should not be in tarball", f)
			}
		}

		found := false
		for _, f := range files {
			if f == "visible.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Error("visible.txt should be in tarball")
		}
	})

	t.Run("dockerignore patterns are respected", func(t *testing.T) {
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".dockerignore"), "*.log\nbuild/\n")
		writeFile(t, filepath.Join(dir, "app.go"), "package main\n")
		writeFile(t, filepath.Join(dir, "debug.log"), "log data\n")
		mkdirAll(t, filepath.Join(dir, "build"))
		writeFile(t, filepath.Join(dir, "build", "output"), "binary\n")
		writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		fileSet := make(map[string]bool)
		for _, f := range files {
			fileSet[f] = true
		}

		if fileSet["debug.log"] {
			t.Error("debug.log should be excluded by .dockerignore pattern *.log")
		}
		if fileSet["build"] || fileSet["build/output"] {
			t.Error("build/ should be excluded by .dockerignore")
		}
		if !fileSet["app.go"] {
			t.Error("app.go should be included")
		}
		if !fileSet["Dockerfile"] {
			t.Error("Dockerfile should always be included")
		}
	})

	t.Run("empty directory produces valid tarball", func(t *testing.T) {
		dir := t.TempDir()

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		if len(files) != 0 {
			t.Fatalf("expected empty tarball, got %v", files)
		}
	})

	t.Run("file contents are preserved", func(t *testing.T) {
		dir := t.TempDir()
		content := "package main\n\nfunc main() {}\n"
		writeFile(t, filepath.Join(dir, "main.go"), content)

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		fileContents := extractTarballContents(t, buf)
		got, ok := fileContents["main.go"]
		if !ok {
			t.Fatal("main.go not found in tarball")
		}
		if got != content {
			t.Errorf("content mismatch:\ngot:  %q\nwant: %q", got, content)
		}
	})

	t.Run("runos manifests are unconditionally excluded", func(t *testing.T) {
		// A multi-yaml directory (staging + prod sharing one source tree)
		// must not bleed either yaml or its overrides into the source
		// archive uploaded for either app. The walker enforces this even
		// without a .dockerignore — that's the security guarantee.
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, "runos.yaml"), "app: a\n")
		writeFile(t, filepath.Join(dir, "runos.mycluster3.appid4.yaml"), "app: a\n")
		writeFile(t, filepath.Join(dir, "runos.mycluster2.appid5.yml"), "app: a\n")
		mkdirAll(t, filepath.Join(dir, "overrides"))
		writeFile(t, filepath.Join(dir, "overrides", "patch.yaml"), "spec: {}\n")
		writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine\n")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		fileSet := make(map[string]bool)
		for _, f := range files {
			fileSet[f] = true
		}
		for _, leak := range []string{
			"runos.yaml",
			"runos.mycluster3.appid4.yaml",
			"runos.mycluster2.appid5.yml",
			"overrides",
			"overrides/patch.yaml",
		} {
			if fileSet[leak] {
				t.Errorf("%q should be excluded from tarball; got %v", leak, files)
			}
		}
		for _, want := range []string{"Dockerfile", "main.go"} {
			if !fileSet[want] {
				t.Errorf("%q should be in tarball; got %v", want, files)
			}
		}
	})

	t.Run("per-app runos.* subdirectories are pruned", func(t *testing.T) {
		// Directory-per-app shape: app B in runos.mycluster2.appid5/ deploys with
		// sourceDir: ".." which puts the walker at the project root. App
		// A's runos.mycluster3.appid4/ subdir must not be walked or written, so
		// nothing inside it (yaml, env, secrets) leaks into B's archive,
		// and no empty runos.mycluster3.appid4/ dir entry shows up in the tarball.
		dir := t.TempDir()

		// Project root: source files plus a sibling app's per-app dir.
		writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine\n")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
		writeFile(t, filepath.Join(dir, "go.mod"), "module x\n")

		appA := filepath.Join(dir, "runos.mycluster3.appid4")
		mkdirAll(t, appA)
		writeFile(t, filepath.Join(appA, "runos.yaml"), "app: a\n")
		writeFile(t, filepath.Join(appA, "should-not-leak.txt"), "secret\n")
		mkdirAll(t, filepath.Join(appA, "nested"))
		writeFile(t, filepath.Join(appA, "nested", "deep.txt"), "deeper secret\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		files := extractTarballFiles(t, buf)
		fileSet := make(map[string]bool, len(files))
		for _, f := range files {
			fileSet[f] = true
		}
		// Source files at the root must be present.
		for _, want := range []string{"Dockerfile", "main.go", "go.mod"} {
			if !fileSet[want] {
				t.Errorf("source file %q should be in tarball; got %v", want, files)
			}
		}
		// The pruned per-app subdir must not appear at all (no empty dir
		// entry, no children of any kind).
		for _, f := range files {
			if strings.HasPrefix(f, "runos.mycluster3.appid4") {
				t.Errorf("per-app subdir leaked into tarball: %q", f)
			}
		}
	})

	t.Run("runos env and source-version sidecars stay excluded", func(t *testing.T) {
		// These are already covered by the hidden-file rule, but assert
		// explicitly so a future relaxation of isHidden doesn't silently
		// start uploading per-app secrets.
		dir := t.TempDir()

		writeFile(t, filepath.Join(dir, ".runos.mycluster3.appid4.env"), "SECRET=x\n")
		writeFile(t, filepath.Join(dir, ".runos.mycluster3.appid4.source-version"), "abc123\n")
		writeFile(t, filepath.Join(dir, ".runos-source-version"), "legacy\n")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

		buf, err := CreateTarball(dir)
		if err != nil {
			t.Fatalf("CreateTarball error: %v", err)
		}

		for _, f := range extractTarballFiles(t, buf) {
			if strings.HasPrefix(filepath.Base(f), ".runos") {
				t.Errorf("runos sidecar %q must not appear in tarball", f)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// LoadConfig() integration
// ---------------------------------------------------------------------------

func TestLoadConfig(t *testing.T) {
	t.Run("valid yaml file", func(t *testing.T) {
		dir := t.TempDir()
		content := `app: myapp
port: 3000
domain: example.com
`
		path := filepath.Join(dir, "runos.yaml")
		writeFile(t, path, content)

		config, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if config.App != "myapp" {
			t.Errorf("expected app=myapp, got %s", config.App)
		}
		if config.Port != 3000 {
			t.Errorf("expected port=3000, got %d", config.Port)
		}
		if len(config.Domain) != 1 || config.Domain[0] != "example.com" {
			t.Errorf("expected domain=[example.com], got %v", config.Domain)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadConfig("/nonexistent/path/runos.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
		if !contains(err.Error(), "not found") {
			t.Fatalf("expected 'not found' in error, got %q", err.Error())
		}
	})

	t.Run("invalid yaml returns error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "runos.yaml")
		writeFile(t, path, ":::invalid yaml:::")

		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})

	t.Run("valid yaml but missing required fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "runos.yaml")
		writeFile(t, path, "cid: some-cluster\n")

		_, err := LoadConfig(path)
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !contains(err.Error(), "app name is required") {
			t.Fatalf("expected 'app name is required' in error, got %q", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// contains checks if substr is found within s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchString(s, substr)))
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// writeFile creates a file with the given content, failing the test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}
}

// mkdirAll creates a directory (and parents), failing the test on error.
func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("failed to create directory %s: %v", path, err)
	}
}

// captureStderr swaps os.Stderr for a pipe while fn runs, then returns
// everything fn wrote. Restores the real Stderr unconditionally.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = prev })

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("close pipe reader: %v", err)
	}
	return out
}

// extractTarballFiles reads a gzipped tarball and returns all entry names.
func extractTarballFiles(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	gzReader, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	var files []string
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %v", err)
		}
		files = append(files, header.Name)
	}
	return files
}

// extractTarballContents reads a gzipped tarball and returns a map of filename to content.
func extractTarballContents(t *testing.T, buf *bytes.Buffer) map[string]string {
	t.Helper()
	gzReader, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	contents := make(map[string]string)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to read tar entry: %v", err)
		}
		if header.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tarReader)
			if err != nil {
				t.Fatalf("failed to read file %s: %v", header.Name, err)
			}
			contents[header.Name] = string(data)
		}
	}
	return contents
}
