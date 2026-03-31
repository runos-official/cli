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
	"testing"

	"gopkg.in/yaml.v3"
)

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
		writeFile(t, filepath.Join(dir, ".runos.test-cluster.env"), content)

		envVars, err := LoadEnvFile(dir, "test-cluster")
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
		dir := t.TempDir()
		envVars, err := LoadEnvFile(dir, "nonexistent")
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
		writeFile(t, filepath.Join(dir, ".runos.cid1.env"), content)

		envVars, err := LoadEnvFile(dir, "cid1")
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
		writeFile(t, filepath.Join(dir, ".runos.c1.env"), `VAL="hello world"`)

		envVars, err := LoadEnvFile(dir, "c1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars["VAL"] != "hello world" {
			t.Errorf("got %q, want %q", envVars["VAL"], "hello world")
		}
	})

	t.Run("single-quoted values have quotes stripped", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".runos.c2.env"), `VAL='hello world'`)

		envVars, err := LoadEnvFile(dir, "c2")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if envVars["VAL"] != "hello world" {
			t.Errorf("got %q, want %q", envVars["VAL"], "hello world")
		}
	})

	t.Run("path traversal CID is rejected", func(t *testing.T) {
		dir := t.TempDir()
		_, err := LoadEnvFile(dir, "../etc/passwd")
		if err == nil {
			t.Fatal("expected error for path traversal CID, got nil")
		}
		if !contains(err.Error(), "invalid cluster ID") {
			t.Fatalf("expected 'invalid cluster ID' error, got %q", err.Error())
		}
	})

	t.Run("CID with backslash is rejected", func(t *testing.T) {
		dir := t.TempDir()
		_, err := LoadEnvFile(dir, `foo\bar`)
		if err == nil {
			t.Fatal("expected error for backslash in CID, got nil")
		}
		if !contains(err.Error(), "invalid cluster ID") {
			t.Fatalf("expected 'invalid cluster ID' error, got %q", err.Error())
		}
	})

	t.Run("CID with dots is rejected", func(t *testing.T) {
		dir := t.TempDir()
		_, err := LoadEnvFile(dir, "foo..bar")
		if err == nil {
			t.Fatal("expected error for dots in CID, got nil")
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
		// **/node_modules — matches at any depth
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

		// **/*.log — match files with extension at any depth
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

		// prefix/**/suffix — match with prefix directory
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

		// prefix/** — match everything under prefix
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
