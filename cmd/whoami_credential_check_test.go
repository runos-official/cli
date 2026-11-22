package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

func credentialCheckTestConfig(t *testing.T, apiURL string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	return &config.Config{ConductorURL: apiURL, APIKey: "stored-secret", AccountID: "acct1"}
}

// T2: a working credential.
func TestCheckSecretCredentialAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/accounts" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		respondJSON(w, http.StatusOK, `{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}]}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != true || extra["verified_reason"] != verifiedAccepted {
		t.Fatalf("extra = %v, want verified=true/accepted", extra)
	}
	accounts, ok := extra["accounts"].([]api.UserAccount)
	if !ok || len(accounts) != 1 || accounts[0].AID != "acct1" || accounts[0].AccountRole != "limited" {
		t.Fatalf("accounts = %v", extra["accounts"])
	}
}

// Review finding 1557: a 200 is not proof the body is the accounts
// envelope (a transparent proxy or captive portal, or a misrouted
// RUNOS_API_URL, can answer 200 with something else entirely). Nothing
// judged the credential in that case, so this must be the unreachable
// state, not accepted.
func TestCheckSecretCredentialNonEnvelope200IsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html>proxy</html>`))
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != false || extra["verified_reason"] != verifiedUnreachable {
		t.Fatalf("extra = %v, want verified=false/unreachable", extra)
	}
}

// A 200 with valid JSON but no "accounts" field at all must be treated
// the same way: the field is missing, not "no accounts".
func TestCheckSecretCredential200WithNoAccountsFieldIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, `{"somethingElse":true}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != false || extra["verified_reason"] != verifiedUnreachable {
		t.Fatalf("extra = %v, want verified=false/unreachable", extra)
	}
}

// T3: three refused rows (revoked, expired, no reason), checked through
// the shared formatAuthError-backed renderer, exit 1.
func TestCheckSecretCredentialRefused(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "revoked", body: `{"error":"Invalid token","reason":"revoked","revokedAt":"2026-05-12T10:11:12Z"}`, want: "revoked at 2026-05-12T10:11:12Z"},
		{name: "expired", body: `{"error":"Invalid token","reason":"expired","expiredAt":"2026-05-12T10:11:12Z"}`, want: "expired at 2026-05-12T10:11:12Z"},
		{name: "unknown reason", body: `{"error":"Invalid token"}`, want: "the server did not say why"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respondJSON(w, http.StatusUnauthorized, tc.body)
			}))
			defer server.Close()
			cfg := credentialCheckTestConfig(t, server.URL)

			extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
			if err == nil {
				t.Fatal("expected an error for a 401 refusal")
			}
			if extra != nil {
				t.Fatalf("expected no fields alongside an error, got %v", extra)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if tc.name == "unknown reason" && strings.Contains(err.Error(), "was it revoked, rotated or expired?") {
				t.Errorf("must not assert revocation with no reason, got %q", err.Error())
			}
		})
	}
}

// T4: a human-only 403 (the route needs a human sign-in) is reported,
// not asserted as revoked or expired, and exits 0.
func TestCheckSecretCredentialNotJudged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusForbidden, `{"error":"Forbidden: API keys cannot perform this action"}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != false || extra["verified_reason"] != verifiedHumanSignInRequired {
		t.Fatalf("extra = %v, want verified=false/human_sign_in_required", extra)
	}
	if note, _ := extra["note"].(string); strings.Contains(note, "revoked") || strings.Contains(note, "expired") {
		t.Errorf("note must not mention revocation or expiry, got %q", note)
	}
}

// T5: the server is unreachable.
func TestCheckSecretCredentialUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := server.URL
	server.Close() // closed listener: nothing answers

	cfg := credentialCheckTestConfig(t, addr)
	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != false || extra["verified_reason"] != verifiedUnreachable {
		t.Fatalf("extra = %v, want verified=false/unreachable", extra)
	}
	if note, _ := extra["note"].(string); !strings.Contains(note, "not reached") {
		t.Errorf("note must say the server was not reached, got %q", note)
	}
}

// Unreachable also covers a 5xx: the server answered, but not with
// anything that judged the credential.
func TestCheckSecretCredential5xxIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusServiceUnavailable, `{"error":"try again"}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified_reason"] != verifiedUnreachable {
		t.Fatalf("extra = %v, want verified_reason=unreachable", extra)
	}
}

// Criterion 4: the merged report (whoamiReport plus the credential
// check's own fields) never carries the secret, whatever the server
// answers.
func TestWhoamiMergedReportNeverCarriesASecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, `{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}]}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)
	cfg.APIKey = "pat-secret-value"

	report := whoamiReport(cfg, auth.CredentialStoredKey, nil)
	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for k, v := range extra {
		report[k] = v
	}
	for k, v := range report {
		if s, ok := v.(string); ok && strings.Contains(s, "secret") {
			t.Fatalf("report field %s carries a secret: %q", k, s)
		}
	}
}

// T9 (partial): the exit codes and the three states are documented.
func TestWhoamiHelpDocumentsExitCodes(t *testing.T) {
	for _, want := range []string{verifiedAccepted, verifiedHumanSignInRequired, verifiedUnreachable, "exit 0", "exit 1"} {
		if !strings.Contains(whoamiCmd.Long, want) {
			t.Errorf("whoami help omits %q:\n%s", want, whoamiCmd.Long)
		}
	}
}
