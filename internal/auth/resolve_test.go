package auth

import (
	"testing"

	"github.com/runos-official/cli/internal/config"
)

func TestResolveToken_PrefersAPIKeyEnv(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "pat_test_token_123")
	// Pass a nil cfg to prove the env path doesn't dereference Firebase.
	got, err := ResolveToken(nil)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "pat_test_token_123" {
		t.Errorf("expected env token verbatim, got %q", got)
	}
}

func TestResolveToken_PartialFirebaseConfigStillUsesEnvFirst(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "pat_env_wins")
	cfg := &config.Config{
		Firebase:     &config.FirebaseConfig{APIKey: "fb-key"},
		RefreshToken: "fb-refresh",
	}
	got, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "pat_env_wins" {
		t.Errorf("env should win over Firebase config, got %q", got)
	}
}

func TestResolveToken_NoCredsErrors(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	_, err := ResolveToken(&config.Config{})
	if err == nil {
		t.Fatal("expected error when neither env nor Firebase config is set")
	}
}

func TestResolveToken_NilCfgWithoutEnvErrors(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	_, err := ResolveToken(nil)
	if err == nil {
		t.Fatal("expected error when cfg is nil and no env var is set")
	}
}

func TestValidateAuthEnvVars(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantError bool
	}{
		{"both unset", map[string]string{}, false},
		{"both set", map[string]string{"RUNOS_API_KEY": "pat", "RUNOS_ACCOUNT_ID": "acct"}, false},
		{"key set, account unset", map[string]string{"RUNOS_API_KEY": "pat"}, false},
		{"key explicit empty", map[string]string{"RUNOS_API_KEY": ""}, true},
		{"account explicit empty", map[string]string{"RUNOS_ACCOUNT_ID": ""}, true},
		{"both explicit empty", map[string]string{"RUNOS_API_KEY": "", "RUNOS_ACCOUNT_ID": ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) {
				v, ok := tc.env[k]
				return v, ok
			}
			err := ValidateAuthEnvVars(lookup)
			if tc.wantError && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestUsingAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	if UsingAPIKey() {
		t.Error("expected false when env unset")
	}
	t.Setenv(APIKeyEnvVar, "anything")
	if !UsingAPIKey() {
		t.Error("expected true when env set")
	}
}
