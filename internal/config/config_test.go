package config

import (
	"encoding/json"
	"os"
	"testing"
)

// Bug 85 regression: both non-PAT login paths route through
// ApplySessionLogin, which MUST clear a leftover stored PAT. Without
// the clear, ResolveToken would rank the stale api_key above the fresh
// Firebase session and every call would 401.
func TestApplySessionLogin_ClearsStoredAPIKey(t *testing.T) {
	cfg := &Config{
		APIKey:    "old-pat",
		AccountID: "oldacct",
	}
	fb := &FirebaseConfig{APIKey: "fb-key", AuthDomain: "d", ProjectID: "p"}
	cfg.ApplySessionLogin("newacct", fb, "fresh-refresh", "2026-06-08T00:00:00Z")

	if cfg.APIKey != "" {
		t.Errorf("api_key must be cleared after a non-PAT login, got %q", cfg.APIKey)
	}
	if cfg.RefreshToken != "fresh-refresh" {
		t.Errorf("refresh_token = %q, want fresh-refresh", cfg.RefreshToken)
	}
	if cfg.AccountID != "newacct" {
		t.Errorf("account_id = %q, want newacct", cfg.AccountID)
	}
	if cfg.Firebase != fb {
		t.Error("firebase config not set")
	}
	if cfg.SignedInAt != "2026-06-08T00:00:00Z" {
		t.Errorf("signed_in_at = %q, want 2026-06-08T00:00:00Z", cfg.SignedInAt)
	}
}

func TestGetConsoleURL(t *testing.T) {
	tests := []struct {
		name       string
		configURL  string
		envURL     string
		wantResult string
	}{
		{
			name:       "returns config value when env var is not set",
			configURL:  "https://console.runos.com",
			envURL:     "",
			wantResult: "https://console.runos.com",
		},
		{
			name:       "returns env var when set",
			configURL:  "https://console.runos.com",
			envURL:     "https://custom-console.example.com",
			wantResult: "https://custom-console.example.com",
		},
		{
			name:       "returns env var even when config is empty",
			configURL:  "",
			envURL:     "https://custom-console.example.com",
			wantResult: "https://custom-console.example.com",
		},
		{
			name:       "returns empty when both are empty",
			configURL:  "",
			envURL:     "",
			wantResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env before each subtest, restore after.
			os.Unsetenv("CONSOLE_URL")
			if tt.envURL != "" {
				t.Setenv("CONSOLE_URL", tt.envURL)
			}

			cfg := &Config{ConsoleURL: tt.configURL}
			got := cfg.GetConsoleURL()
			if got != tt.wantResult {
				t.Errorf("GetConsoleURL() = %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestGetAPIURL(t *testing.T) {
	tests := []struct {
		name       string
		configURL  string
		envURL     string
		wantResult string
	}{
		{
			name:       "returns config value when env var is not set",
			configURL:  "https://conductor.runos.com",
			envURL:     "",
			wantResult: "https://conductor.runos.com",
		},
		{
			name:       "returns env var when set",
			configURL:  "https://conductor.runos.com",
			envURL:     "https://custom-conductor.example.com",
			wantResult: "https://custom-conductor.example.com",
		},
		{
			name:       "returns env var even when config is empty",
			configURL:  "",
			envURL:     "https://custom-conductor.example.com",
			wantResult: "https://custom-conductor.example.com",
		},
		{
			name:       "returns empty when both are empty",
			configURL:  "",
			envURL:     "",
			wantResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("RUNOS_API_URL")
			if tt.envURL != "" {
				t.Setenv("RUNOS_API_URL", tt.envURL)
			}

			cfg := &Config{ConductorURL: tt.configURL}
			got := cfg.GetAPIURL()
			if got != tt.wantResult {
				t.Errorf("GetAPIURL() = %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestGetDefaultClusterID(t *testing.T) {
	tests := []struct {
		name       string
		configID   string
		envID      string
		wantResult string
	}{
		{
			name:       "returns config value when env var is not set",
			configID:   "cluster-abc",
			envID:      "",
			wantResult: "cluster-abc",
		},
		{
			name:       "returns env var when set",
			configID:   "cluster-abc",
			envID:      "cluster-override",
			wantResult: "cluster-override",
		},
		{
			name:       "returns env var even when config is empty",
			configID:   "",
			envID:      "cluster-override",
			wantResult: "cluster-override",
		},
		{
			name:       "returns empty when both are empty",
			configID:   "",
			envID:      "",
			wantResult: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("RUNOS_CLUSTER_ID")
			if tt.envID != "" {
				t.Setenv("RUNOS_CLUSTER_ID", tt.envID)
			}

			cfg := &Config{DefaultClusterID: tt.configID}
			got := cfg.GetDefaultClusterID()
			if got != tt.wantResult {
				t.Errorf("GetDefaultClusterID() = %q, want %q", got, tt.wantResult)
			}
		})
	}
}

func TestConfigJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "full config",
			config: Config{
				Env:              "production",
				ConsoleURL:       "https://console.runos.com",
				ConductorURL:     "https://conductor.runos.com",
				AccountID:        "acc-123",
				DefaultClusterID: "cluster-456",
				RefreshToken:     "some-refresh-token",
				Firebase: &FirebaseConfig{
					APIKey:     "firebase-key",
					AuthDomain: "runos.firebaseapp.com",
					ProjectID:  "runos-prod",
				},
				SignedInAt: "2026-03-31T12:00:00Z",
			},
		},
		{
			name: "minimal config",
			config: Config{
				Env:          "dev",
				ConsoleURL:   "https://dev.runos.com",
				ConductorURL: "https://dev-api.runos.com",
			},
		},
		{
			name:   "empty config",
			config: Config{},
		},
		{
			name: "config without firebase",
			config: Config{
				Env:              "staging",
				ConsoleURL:       "https://staging.runos.com",
				ConductorURL:     "https://staging-api.runos.com",
				AccountID:        "acc-789",
				DefaultClusterID: "cluster-xyz",
				RefreshToken:     "refresh-tok",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.MarshalIndent(&tt.config, "", "  ")
			if err != nil {
				t.Fatalf("failed to marshal config: %v", err)
			}

			var got Config
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("failed to unmarshal config: %v", err)
			}

			// Compare scalar fields.
			if got.Env != tt.config.Env {
				t.Errorf("Env = %q, want %q", got.Env, tt.config.Env)
			}
			if got.ConsoleURL != tt.config.ConsoleURL {
				t.Errorf("ConsoleURL = %q, want %q", got.ConsoleURL, tt.config.ConsoleURL)
			}
			if got.ConductorURL != tt.config.ConductorURL {
				t.Errorf("ConductorURL = %q, want %q", got.ConductorURL, tt.config.ConductorURL)
			}
			if got.AccountID != tt.config.AccountID {
				t.Errorf("AccountID = %q, want %q", got.AccountID, tt.config.AccountID)
			}
			if got.DefaultClusterID != tt.config.DefaultClusterID {
				t.Errorf("DefaultClusterID = %q, want %q", got.DefaultClusterID, tt.config.DefaultClusterID)
			}
			if got.RefreshToken != tt.config.RefreshToken {
				t.Errorf("RefreshToken = %q, want %q", got.RefreshToken, tt.config.RefreshToken)
			}
			if got.SignedInAt != tt.config.SignedInAt {
				t.Errorf("SignedInAt = %q, want %q", got.SignedInAt, tt.config.SignedInAt)
			}

			// Compare Firebase config.
			if tt.config.Firebase == nil {
				if got.Firebase != nil {
					t.Errorf("Firebase = %+v, want nil", got.Firebase)
				}
			} else {
				if got.Firebase == nil {
					t.Fatalf("Firebase = nil, want %+v", tt.config.Firebase)
				}
				if got.Firebase.APIKey != tt.config.Firebase.APIKey {
					t.Errorf("Firebase.APIKey = %q, want %q", got.Firebase.APIKey, tt.config.Firebase.APIKey)
				}
				if got.Firebase.AuthDomain != tt.config.Firebase.AuthDomain {
					t.Errorf("Firebase.AuthDomain = %q, want %q", got.Firebase.AuthDomain, tt.config.Firebase.AuthDomain)
				}
				if got.Firebase.ProjectID != tt.config.Firebase.ProjectID {
					t.Errorf("Firebase.ProjectID = %q, want %q", got.Firebase.ProjectID, tt.config.Firebase.ProjectID)
				}
			}
		})
	}
}
