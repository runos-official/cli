package config

import (
	"path/filepath"
	"strings"
	"testing"
)

/*
A test binary never resolves the real ~/.runos.

A test that forgot t.Setenv("HOME", t.TempDir()) read the developer's real config: the real person
id, the real identity service and the real account. Every later step then acted on that sign-in. The
guard makes the forgotten line a loud failure. These cases never read or write the directory they
name.
*/
func TestDirRefusesAHomeOutsideTheTempDirInATestBinary(t *testing.T) {
	for _, home := range []string{"/Users/someone", "/home/someone", "/"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			dir, err := Dir()
			if err == nil {
				t.Fatalf("Dir() = %q with HOME=%s in a test binary, want a refusal", dir, home)
			}
			if !strings.Contains(err.Error(), "HOME") {
				t.Fatalf("the refusal does not tell the test what to set: %v", err)
			}
		})
	}
}

func TestDirAcceptsATempHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir() with a temp HOME: %v", err)
	}
	if want := filepath.Join(home, configDirName); dir != want {
		t.Fatalf("Dir() = %q, want %q", dir, want)
	}
}

// Load must refuse too, not fall through to the CI env-var path, so a forgotten HOME is never quiet.
func TestLoadRefusesAHomeOutsideTheTempDir(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	t.Setenv("RUNOS_API_KEY", "test-key")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("Load() = %v, want the HOME refusal", err)
	}
}

// HomeDir is the opener for every path under home, so it refuses on its own as well.
func TestHomeDirRefusesAHomeOutsideTheTempDirInATestBinary(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	if home, err := HomeDir(); err == nil {
		t.Fatalf("HomeDir() = %q in a test binary, want a refusal", home)
	}
}
