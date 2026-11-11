package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitConfigDirIsolatesState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("RUNOS_CONFIG_DIR", root)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != root {
		t.Fatalf("got %s, want private directory %s", dir, root)
	}
	cfg := &Config{ConductorURL: "https://api.dev.example", AccountID: "private-account"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || loaded.AccountID != cfg.AccountID {
		t.Fatalf("private config roundtrip: %v", err)
	}
}

func TestExplicitConfigDirMustBeAbsolute(t *testing.T) {
	t.Setenv("RUNOS_CONFIG_DIR", "relative")
	if _, err := Dir(); err == nil {
		t.Fatal("relative config directory accepted")
	}
}
