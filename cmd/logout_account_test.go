package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/config"
)

func TestLogoutClearsMetadataOnlyActiveAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".runos")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{KnownAccounts: []config.KnownAccount{{AccountID: "acct", Active: true}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.KnownAccounts[0].Active {
		t.Fatal("logout left the known account active")
	}
}
