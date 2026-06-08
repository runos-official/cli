package cmd

import (
	"testing"

	"github.com/runos-official/cli/internal/config"
)

// Bug 87 regression: `runos status` must treat a stored PAT or
// RUNOS_API_KEY as authenticated, not only the Firebase fields. Pre-fix
// the auth check was gated on `cfg.RefreshToken != "" && cfg.Firebase
// != nil`, so a PAT-only config printed "Not logged in" while every
// authenticated command worked. resolveAuthMethod mirrors
// auth.ResolveToken's RUNOS_API_KEY -> stored PAT -> Firebase priority.
func TestResolveAuthMethod(t *testing.T) {
	firebase := &config.FirebaseConfig{APIKey: "fb-key"}
	tests := []struct {
		name      string
		cfg       *config.Config
		apiKeyEnv string
		want      authMethod
	}{
		{
			name:      "RUNOS_API_KEY wins over everything",
			cfg:       &config.Config{APIKey: "stored", RefreshToken: "r", Firebase: firebase},
			apiKeyEnv: "env-pat",
			want:      authPATEnv,
		},
		{
			name: "stored PAT, no firebase (the bug repro)",
			cfg:  &config.Config{APIKey: "stored-pat"},
			want: authPATStored,
		},
		{
			name: "stored PAT wins over firebase fields",
			cfg:  &config.Config{APIKey: "stored-pat", RefreshToken: "r", Firebase: firebase},
			want: authPATStored,
		},
		{
			name: "firebase session when no PAT present",
			cfg:  &config.Config{RefreshToken: "r", Firebase: firebase},
			want: authFirebase,
		},
		{
			name: "refresh token without firebase block is not authenticated",
			cfg:  &config.Config{RefreshToken: "r"},
			want: authNone,
		},
		{
			name: "empty config is not authenticated",
			cfg:  &config.Config{},
			want: authNone,
		},
		{
			name:      "whitespace-only env PAT is ignored",
			cfg:       &config.Config{APIKey: "stored-pat"},
			apiKeyEnv: "   ",
			want:      authPATStored,
		},
		{
			name: "whitespace-only stored PAT is ignored",
			cfg:  &config.Config{APIKey: "   ", RefreshToken: "r", Firebase: firebase},
			want: authFirebase,
		},
		{
			name: "nil config is not authenticated",
			cfg:  nil,
			want: authNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAuthMethod(tt.cfg, tt.apiKeyEnv); got != tt.want {
				t.Errorf("resolveAuthMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}
