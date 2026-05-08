package jobs

import (
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// Regression test for V11 (VCS_DEPLOY_TEST_NOTES.md): jobs.Service must
// honour RUNOS_API_KEY for authentication, not demand a Firebase refresh
// token. Pre-fix: `runos deploy --follow` and `runos follow <jobId>` failed
// with "authentication required: run 'runos login'" in CI even though the
// PAT was set, because getAuthToken bypassed auth.ResolveToken and
// hard-required cfg.Firebase. The deploy POST itself worked because
// cmd/deploy.go correctly used auth.ResolveToken; the follow path didn't.
//
// Fix: getAuthToken now delegates to auth.ResolveToken, which already
// has the documented "RUNOS_API_KEY wins, Firebase falls back" contract
// and has its own tests in internal/auth/resolve_test.go.
func TestGetAuthToken_UsesAPIKeyWhenSet(t *testing.T) {
	t.Setenv(auth.APIKeyEnvVar, "pat_test_token_v11")

	// Empty config (no Firebase, no refresh token). Pre-fix this would
	// error out because getAuthToken short-circuits on cfg.Firebase==nil.
	cfg := &config.Config{}
	got, err := getAuthToken(cfg)
	if err != nil {
		t.Fatalf("getAuthToken: %v", err)
	}
	if got != "pat_test_token_v11" {
		t.Errorf("expected the API key verbatim, got %q", got)
	}
}

// Companion test: when no PAT is set and no Firebase config exists, the
// caller should still see a clear authentication error (not a silent
// success or a panic from a nil dereference).
func TestGetAuthToken_NoCredsErrorsClearly(t *testing.T) {
	t.Setenv(auth.APIKeyEnvVar, "")
	_, err := getAuthToken(&config.Config{})
	if err == nil {
		t.Fatal("expected error when neither RUNOS_API_KEY nor Firebase config is set")
	}
}
