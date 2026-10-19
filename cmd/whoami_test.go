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
	for _, kind := range []auth.CredentialKind{auth.CredentialStoredPAT, auth.CredentialInteractive} {
		got := whoamiReport(cfg, kind, nil)
		for k, v := range got {
			if s, ok := v.(string); ok && (strings.Contains(s, "secret")) {
				t.Fatalf("%s report field %s carries a secret: %q", kind, k, s)
			}
		}
	}
}

func TestWhoamiErrorForNoCredentialSaysHowToSignIn(t *testing.T) {
	err := whoamiNotSignedIn()
	if !errors.Is(err, auth.ErrNotAuthenticated) || !strings.Contains(err.Error(), "runos login") {
		t.Fatalf("err = %v", err)
	}
}
