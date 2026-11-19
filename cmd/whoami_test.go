package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"
)

func TestWhoamiReportForARoidSignIn(t *testing.T) {
	cfg := &config.Config{AuthIssuer: "https://id.example.com", Env: "dev"}
	cfg.ApplyRoidLogin("https://id.example.com", "p_alice", "acct1", "2026-10-15T00:00:00Z")
	got := whoamiReport(cfg, auth.CredentialRoid, &roidauth.Identity{PersonID: "p_alice", Method: "password", Assurance: 2, Accounts: []string{"acct1", "acct2"}})
	if got["credential"] != "roid" || got["person_id"] != "p_alice" || got["account_id"] != "acct1" || got["auth_issuer"] != "https://id.example.com" {
		t.Fatalf("report = %v", got)
	}
	if accts, _ := got["accounts"].([]string); len(accts) != 2 {
		t.Fatalf("accounts = %v", got["accounts"])
	}
}

func TestWhoamiReportNeverCarriesASecret(t *testing.T) {
	cfg := &config.Config{RefreshToken: "fb-refresh-secret", APIKey: "pat-secret", AccountID: "acct1", SignedInAt: "2026-10-15T00:00:00Z"}
	for _, kind := range []auth.CredentialKind{auth.CredentialStoredKey, auth.CredentialInteractive} {
		got := whoamiReport(cfg, kind, nil)
		for k, v := range got {
			if s, ok := v.(string); ok && (strings.Contains(s, "secret")) {
				t.Fatalf("%s report field %s carries a secret: %q", kind, k, s)
			}
		}
	}
}

// Story 343 / FCR2030: a stored secret never gets a verified semantic
// type from this command, so the report must say "unknown" rather than
// imply a Personal Access Token -- the thing an Account API Key under
// the exact same source would be mislabelled as.
func TestWhoamiReportCredentialTypeIsUnknownForAStoredSecret(t *testing.T) {
	cfg := &config.Config{APIKey: "secret", AccountID: "acct1", SignedInAt: "2026-10-15T00:00:00Z"}
	got := whoamiReport(cfg, auth.CredentialStoredKey, nil)
	if got["credential_type"] != "unknown" {
		t.Fatalf("credential_type = %v, want %q", got["credential_type"], "unknown")
	}
}

// A roid sign-in and an interactive session are never secret-sourced,
// so neither carries a credential_type field: that field exists only to
// flag an UNVERIFIED semantic type on a secret, not to describe every
// credential.
func TestWhoamiReportOmitsCredentialTypeForANonSecretSource(t *testing.T) {
	cfg := &config.Config{AccountID: "acct1"}
	for _, kind := range []auth.CredentialKind{auth.CredentialInteractive, auth.CredentialRoid, auth.CredentialNone} {
		got := whoamiReport(cfg, kind, nil)
		if _, present := got["credential_type"]; present {
			t.Fatalf("%s: credential_type must not appear, got %v", kind, got["credential_type"])
		}
	}
}

func TestWhoamiErrorForNoCredentialSaysHowToSignIn(t *testing.T) {
	err := whoamiNotSignedIn()
	if !errors.Is(err, auth.ErrNotAuthenticated) || !strings.Contains(err.Error(), "runos login") {
		t.Fatalf("err = %v", err)
	}
}
