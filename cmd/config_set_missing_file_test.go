package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/spf13/cobra"
)

// stubDefaultConfig replaces the CDN fetch with a default environment
// written to the test HOME, so these tests run network-free.
func stubDefaultConfig(t *testing.T, fail bool) {
	t.Helper()
	orig := initDefaultConfig
	t.Cleanup(func() { initDefaultConfig = orig })
	initDefaultConfig = func() (*config.Config, error) {
		if fail {
			return nil, errors.New("fetch failed")
		}
		cfg := &config.Config{}
		cfg.ApplyEnvironment("prod", "https://console.example.com", "https://api.example.com")
		return cfg, cfg.Save()
	}
}

func isolateConfigEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"RUNOS_API_KEY", "RUNOS_API_URL", "RUNOS_ACCOUNT_ID", "RUNOS_CLUSTER_ID", "CONSOLE_URL"} {
		t.Setenv(k, "")
	}
	return home
}

// With no config file, `config set` persists the key on top of the default
// environment, so the API and console URLs that login and every other
// command need are still there afterwards.
func TestRunConfigSet_MissingFileKeepsDefaults(t *testing.T) {
	cases := []struct {
		key, value           string
		wantAPI, wantConsole string
		wantCluster          string
	}{
		{"api-url", "https://api.other.example.com", "https://api.other.example.com", "https://console.example.com", ""},
		{"console-url", "https://console.other.example.com", "https://api.example.com", "https://console.other.example.com", ""},
		{"cid", "abc123", "https://api.example.com", "https://console.example.com", "abc123"},
	}
	for _, tt := range cases {
		t.Run(tt.key, func(t *testing.T) {
			isolateConfigEnv(t)
			stubDefaultConfig(t, false)
			if err := runConfigSet(&cobra.Command{}, []string{tt.key, tt.value}); err != nil {
				t.Fatalf("runConfigSet with no config file: %v", err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load after set: %v", err)
			}
			if got := cfg.GetAPIURL(); got != tt.wantAPI {
				t.Errorf("api url = %q, want %q", got, tt.wantAPI)
			}
			if cfg.ConsoleURL != tt.wantConsole {
				t.Errorf("console url = %q, want %q", cfg.ConsoleURL, tt.wantConsole)
			}
			if cfg.DefaultClusterID != tt.wantCluster {
				t.Errorf("cluster id = %q, want %q", cfg.DefaultClusterID, tt.wantCluster)
			}
		})
	}
}

// When the default environment cannot be fetched, the set refuses with a
// message instead of writing a file with no URLs.
func TestRunConfigSet_MissingFileFetchFailureRefuses(t *testing.T) {
	home := isolateConfigEnv(t)
	stubDefaultConfig(t, true)
	err := runConfigSet(&cobra.Command{}, []string{"cid", "abc123"})
	if err == nil || !strings.Contains(err.Error(), "could not be fetched") {
		t.Fatalf("want fetch refusal, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".runos", "config.json")); !os.IsNotExist(statErr) {
		t.Errorf("config file must not exist after refusal, stat err = %v", statErr)
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
			home := isolateConfigEnv(t)
			stubDefaultConfig(t, false)

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
