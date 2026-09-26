package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/deploy"
)

// FCR 754: apps pull --code --app-id unpacked the source into the per-app
// folder but stamped the directory-per-app default `sourceDir: ..`, so a
// deploy from the pulled folder uploaded the (empty) parent. With --code the
// default must be "", because the source lands beside the yaml.
func TestPulledSourceDirDefault(t *testing.T) {
	cases := []struct {
		def  string
		code bool
		want string
	}{
		{"..", true, ""},
		{"..", false, ".."},
		{"", true, ""},
	}
	for _, c := range cases {
		if got := pulledSourceDirDefault(c.def, c.code); got != c.want {
			t.Errorf("pulledSourceDirDefault(%q, %v) = %q, want %q", c.def, c.code, got, c.want)
		}
	}
}

// An explicit sourceDir (local yaml or server) is where deploy archives
// from, so pull --code must unpack there, not beside the yaml.
func TestPulledCodeRoot(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "runos.c1.a1")
	cases := []struct {
		sourceDir, want string
		wantErr         bool
	}{
		{"", appDir, false},
		{".", appDir, false},
		{"..", filepath.Dir(appDir), false},
		{"src", filepath.Join(appDir, "src"), false},
		{"/abs", "", true},
	}
	for _, c := range cases {
		got, err := pulledCodeRoot(appDir, c.sourceDir)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("pulledCodeRoot(%q) = (%q, %v), want (%q, err=%v)", c.sourceDir, got, err, c.want, c.wantErr)
		}
	}
}

// Pull and deploy must name the same directory for every sourceDir, so a
// deploy from a pulled folder archives the source pull unpacked.
func TestPulledCodeRootMatchesDeployArchiveRoot(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "runos.c1.a1")
	if err := os.MkdirAll(filepath.Join(appDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sd := range []string{"", ".", "..", "src"} {
		pullRoot, err := pulledCodeRoot(appDir, sd)
		if err != nil {
			t.Fatal(err)
		}
		deployRoot, err := deploy.ResolveArchiveRoot(appDir, sd)
		if err != nil {
			t.Fatal(err)
		}
		if pullRoot != deployRoot {
			t.Errorf("sourceDir %q: pull unpacks into %q, deploy archives %q", sd, pullRoot, deployRoot)
		}
	}
}
