package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// Bug 87 regression: `runos status` must treat a stored secret
// credential or RUNOS_API_KEY as authenticated, not only the Firebase
// fields. Pre-fix the auth check was gated on `cfg.RefreshToken != ""
// && cfg.Firebase != nil`, so a secret-only config printed "Not logged
// in" while every authenticated command worked. resolveAuthMethod
// mirrors auth.ResolveToken's RUNOS_API_KEY -> stored secret -> Firebase
// priority.
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
			apiKeyEnv: "env-secret",
			want:      authEnvKey,
		},
		{
			name: "stored secret, no firebase (the bug repro)",
			cfg:  &config.Config{APIKey: "stored-secret"},
			want: authStoredKey,
		},
		{
			name: "stored secret wins over firebase fields",
			cfg:  &config.Config{APIKey: "stored-secret", RefreshToken: "r", Firebase: firebase},
			want: authStoredKey,
		},
		{
			name: "roid sign-in when no secret present, and it wins over firebase fields",
			cfg: &config.Config{AuthIssuer: "https://id.example.test", Roid: &config.RoidSignIn{Issuer: "https://id.example.test", PersonID: "p_alice"},
				RefreshToken: "r", Firebase: firebase},
			want: authRoid,
		},
		{
			name: "a roid sign-in made against another issuer does not count",
			cfg:  &config.Config{AuthIssuer: "https://id.example.test", Roid: &config.RoidSignIn{Issuer: "https://other.example.test", PersonID: "p_alice"}},
			want: authNone,
		},
		{
			name: "stored secret wins over a roid sign-in",
			cfg: &config.Config{APIKey: "stored-secret", AuthIssuer: "https://id.example.test",
				Roid: &config.RoidSignIn{Issuer: "https://id.example.test", PersonID: "p_alice"}},
			want: authStoredKey,
		},
		{
			name: "firebase session when no secret present",
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
			name:      "whitespace-only env secret is ignored",
			cfg:       &config.Config{APIKey: "stored-secret"},
			apiKeyEnv: "   ",
			want:      authStoredKey,
		},
		{
			name: "whitespace-only stored secret is ignored",
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

// Story 343 / FCR2030: statusCredentialType must say "unknown" for
// every secret-sourced method, never a guessed specific type, and must
// not appear at all for a non-secret method (there is no credential
// type question to answer for a sign-in).
func TestStatusCredentialType(t *testing.T) {
	for _, method := range []authMethod{authEnvKey, authStoredKey} {
		if got := statusCredentialType(method); got != auth.CredentialTypeUnknown {
			t.Errorf("statusCredentialType(%q) = %q, want %q", method, got, auth.CredentialTypeUnknown)
		}
	}
	for _, method := range []authMethod{authRoid, authFirebase, authNone} {
		if got := statusCredentialType(method); got != "" {
			t.Errorf("statusCredentialType(%q) = %q, want empty", method, got)
		}
	}
}

// The parenthetical hint on "Authentication: Logged in" must never say
// "PAT": an Account API Key reaches this exact code path (same source,
// unverified type) and a PAT-specific hint would mislabel it.
func TestAuthMethodLabelNeverClaimsPAT(t *testing.T) {
	for _, v := range []any{string(authEnvKey), string(authStoredKey), string(authFirebase), nil, "legacy"} {
		if got := authMethodLabel(v); strings.Contains(got, "PAT") {
			t.Errorf("authMethodLabel(%v) = %q, must not say PAT", v, got)
		}
	}
	if got := authMethodLabel(string(authEnvKey)); !strings.Contains(got, auth.APIKeyEnvVar) {
		t.Errorf("authMethodLabel(env) = %q, want it to name %s", got, auth.APIKeyEnvVar)
	}
}
