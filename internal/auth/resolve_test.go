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

func TestResolveToken_StoredAPIKeyWhenNoEnv(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	cfg := &config.Config{APIKey: "stored-pat"}
	got, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "stored-pat" {
		t.Errorf("expected stored PAT, got %q", got)
	}
}

func TestResolveToken_EnvWinsOverStoredAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "env-pat")
	cfg := &config.Config{APIKey: "stored-pat"}
	got, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "env-pat" {
		t.Errorf("env var must win over stored PAT, got %q", got)
	}
}

// A stored PAT must short-circuit before the Firebase exchange (which
// would otherwise attempt a network call), proving stored-PAT >
// Firebase in the precedence order.
func TestResolveToken_StoredAPIKeyWinsOverFirebase(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	cfg := &config.Config{
		APIKey:       "stored-pat",
		Firebase:     &config.FirebaseConfig{APIKey: "fb-key"},
		RefreshToken: "fb-refresh",
	}
	got, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "stored-pat" {
		t.Errorf("stored PAT must win over Firebase, got %q", got)
	}
}

func TestResolveToken_StoredAPIKeyTrimmed(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	cfg := &config.Config{APIKey: " \tstored-pat\n"}
	got, err := ResolveToken(cfg)
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "stored-pat" {
		t.Errorf("stored PAT should be trimmed, got %q", got)
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

// Issue 110: a PAT pasted with surrounding whitespace (common Slack /
// docs / web-UI copy-paste) used to leak "net/http: invalid header
// field value for Authorization" because net/http refuses CR/LF in
// header values. TrimSpace canonicalises so both the leading-space
// and trailing-newline shapes resolve to the same clean PAT.
func TestResolveToken_TrimsWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trailing newline (the issue 110 repro)", "secret-pat\n", "secret-pat"},
		{"trailing CRLF", "secret-pat\r\n", "secret-pat"},
		{"leading space", " secret-pat", "secret-pat"},
		{"surrounding whitespace", " \tsecret-pat\n", "secret-pat"},
		{"clean PAT untouched", "secret-pat", "secret-pat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(APIKeyEnvVar, tc.in)
			got, err := ResolveToken(nil)
			if err != nil {
				t.Fatalf("ResolveToken: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveToken = %q, want %q", got, tc.want)
			}
			// Symmetric: UsingAPIKey must agree with what ResolveToken
			// considers usable.
			if !UsingAPIKey() {
				t.Errorf("UsingAPIKey() = false, want true (token resolved to %q)", got)
			}
		})
	}
}

// A whitespace-only PAT should trip the explicit-empty refusal, not
// reach the net/http layer.
func TestValidateAuthEnvVars_WhitespaceOnlyPATRefused(t *testing.T) {
	err := ValidateAuthEnvVars(func(name string) (string, bool) {
		if name == APIKeyEnvVar {
			return "\n", true
		}
		return "", false
	})
	if err == nil {
		t.Fatal("expected refusal for whitespace-only PAT")
	}
}
