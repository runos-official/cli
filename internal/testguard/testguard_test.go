package testguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActiveIsTrueInATestBinary(t *testing.T) {
	if !Active() {
		t.Fatal("Active() is false inside go test")
	}
}

func TestUnderTempDir(t *testing.T) {
	tmp := os.TempDir()
	child := t.TempDir()
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"a t.TempDir directory", child, true},
		{"a path below it", filepath.Join(child, ".runos", "config.json"), true},
		{"the temp dir itself", tmp, false},
		{"a sibling that shares the prefix", filepath.Clean(tmp) + "-not-temp", false},
		{"an escape through ..", filepath.Join(child, "..", "..", "..", "etc"), false},
		{"a real-looking home", "/Users/someone", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnderTempDir(c.path); got != c.want {
				t.Fatalf("UnderTempDir(%q) = %v, want %v (temp dir %q)", c.path, got, c.want, tmp)
			}
		})
	}
}

// macOS reaches the temp dir through /var -> /private/var; both spellings must count.
func TestUnderTempDirFollowsSymlinks(t *testing.T) {
	child := t.TempDir()
	resolved, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	if !UnderTempDir(resolved) {
		t.Fatalf("UnderTempDir(%q) = false for the resolved form of %q", resolved, child)
	}
}
