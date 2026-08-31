package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
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
	/*
	 ITS OWN COMMAND, NOT THE PACKAGE GLOBAL, and that matters now.

	 `logout` tells the VPN daemon to take the tunnel down (FPL26 D3). `logoutCmd` is the production
	 command, so its `--socket` carries the production default, and running this test against it
	 ENDED THE DEVELOPER'S LIVE VPN SESSION. Measured 2026-08-31: a plain `go test ./...` left the
	 machine disconnected.

	 Tests in this repo are network-free, and a root daemon on the developer's own machine is the
	 sharpest version of a real host. Every test that drives `runLogout` names a socket that is not
	 there.
	*/
	cmd := &cobra.Command{Use: "logout", RunE: runLogout}
	cmd.Flags().String("socket", "", "")
	cmd.SetArgs([]string{"--socket", "/nonexistent/tests-never-touch-a-real-daemon.sock"})
	if err := cmd.Execute(); err != nil {
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
