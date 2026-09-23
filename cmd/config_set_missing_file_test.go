package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

// Story 279 red step: with HOME set to an empty directory (no config
// file), `runos config set <key> <value>` must persist the setting so a
// later read returns it. Pre-fix runConfigSet returns
// "failed to load config" and creates no file.
func TestRunConfigSet_MissingFilePersistsOnlySetKey(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		check func(t *testing.T, cfg *config.Config)
	}{
		{
			name:  "api-url persists",
			key:   "api-url",
			value: "https://api.example.com",
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if got := cfg.GetAPIURL(); got != "https://api.example.com" {
					t.Errorf("GetAPIURL = %q, want %q", got, "https://api.example.com")
				}
			},
		},
		{
			name:  "console-url persists",
			key:   "console-url",
			value: "https://console.example.com",
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.ConsoleURL != "https://console.example.com" {
					t.Errorf("ConsoleURL = %q, want %q", cfg.ConsoleURL, "https://console.example.com")
				}
			},
		},
		{
			name:  "cid persists",
			key:   "cid",
			value: "abc123",
			check: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if cfg.DefaultClusterID != "abc123" {
					t.Errorf("DefaultClusterID = %q, want %q", cfg.DefaultClusterID, "abc123")
				}
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("RUNOS_API_KEY", "")
			t.Setenv("RUNOS_API_URL", "")
			t.Setenv("RUNOS_ACCOUNT_ID", "")
			t.Setenv("RUNOS_CLUSTER_ID", "")
			t.Setenv("CONSOLE_URL", "")

			if err := runConfigSet(&cobra.Command{}, []string{tt.key, tt.value}); err != nil {
				t.Fatalf("runConfigSet with no config file: unexpected error %v", err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load after set: unexpected error %v", err)
			}
			tt.check(t, cfg)
		})
	}
}

// The shape-2 decision as a pure helper: no CDN fetch runs here, so
// the RUNOS_API_KEY in-memory fallback is testable network-free. A
// loaded config carrying CDN URLs but no file must not reach the disk.
func TestBaseConfigForSet(t *testing.T) {
	loaded := &config.Config{
		ConsoleURL:   "https://console.example.com",
		ConductorURL: "https://api.example.com",
		Env:          "prod",
		EnvAPIURL:    "https://api.example.com",
	}
	cases := []struct {
		name       string
		loaded     *config.Config
		loadErr    error
		exists     bool
		wantLoaded bool
	}{
		{"file present returns loaded config", loaded, nil, true, true},
		{"missing file refusal starts fresh", nil, config.ErrConfigNotFound, false, false},
		{"api-key fallback without file starts fresh", loaded, nil, false, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := baseConfigForSet(tt.loaded, tt.loadErr, tt.exists)
			if tt.wantLoaded {
				if got != tt.loaded {
					t.Errorf("baseConfigForSet must return the loaded config")
				}
				return
			}
			if got == nil {
				t.Fatalf("baseConfigForSet must return a fresh config, got nil")
			}
			if got == tt.loaded {
				t.Errorf("baseConfigForSet must not return the loaded config")
			}
			if got.ConsoleURL != "" || got.ConductorURL != "" || got.Env != "" || got.EnvAPIURL != "" {
				t.Errorf("fresh config must carry no CDN URL, got %+v", got)
			}
		})
	}
}

// Invalid values and unknown keys must error and must not create a file.
func TestRunConfigSet_MissingFileInvalidCreatesNothing(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"cid empty", "cid", ""},
		{"cid wrong shape", "cid", "BAD_ID"},
		{"api-url empty", "api-url", ""},
		{"api-url unparseable", "api-url", "not-a-url"},
		{"api-url non-http scheme", "api-url", "ftp://example.com/x"},
		{"console-url non-http scheme", "console-url", "ftp://example.com/x"},
		{"read-only env", "env", "beta"},
		{"read-only account-id", "account-id", "xyz"},
		{"unknown key", "bogus", "x"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("RUNOS_API_KEY", "")

			err := runConfigSet(&cobra.Command{}, []string{tt.key, tt.value})
			if err == nil {
				t.Fatalf("runConfigSet(%q, %q) with no config file: want error, got nil", tt.key, tt.value)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Errorf("runConfigSet(%q, %q): error text is empty", tt.key, tt.value)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".runos", "config.json")); !os.IsNotExist(statErr) {
				t.Errorf("runConfigSet(%q, %q): config file must not exist after refusal, stat err = %v", tt.key, tt.value, statErr)
			}
		})
	}
}
