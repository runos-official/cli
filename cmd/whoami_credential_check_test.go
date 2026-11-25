package cmd

import (
	"encoding/json"
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

// T1: a 200 that names the caller's own verified credential (contract
// C-4) reports credential_type and credential_id, verbatim, alongside the
// existing accepted fields.
func TestCheckSecretCredentialNamesTheVerifiedCredential(t *testing.T) {
	cases := []struct {
		name           string
		credentialType string
		credentialID   string
	}{
		{"personal access token", "personal_pat", "ckb1a2b3c4d5"},
		{"account api key", "account_api_key", "ckb6f7e8d9c0"},
		{"legacy conductor account key", "legacy_conductor_account_key", "ckb-legacy-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respondJSON(w, http.StatusOK, `{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}],`+
					`"credential":{"credentialType":"`+tc.credentialType+`","credentialId":"`+tc.credentialID+`"}}`)
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
			if extra["credential_type"] != tc.credentialType {
				t.Errorf("credential_type = %v, want %q", extra["credential_type"], tc.credentialType)
			}
			if extra["credential_id"] != tc.credentialID {
				t.Errorf("credential_id = %v, want %q", extra["credential_id"], tc.credentialID)
			}
			if _, present := extra["credential_type_note"]; present {
				t.Errorf("credential_type_note must not appear alongside a named credential, got %v", extra["credential_type_note"])
			}
		})
	}
}

// T2 (pin): no "credential" field, or a JSON null one, leaves the report
// exactly as it was before this story: credential_type stays "unknown" in
// the merged report, and no credential_id appears.
func TestCheckSecretCredentialWithoutTheFieldIsUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}]}`,
		`{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}],"credential":null}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, body)
		}))
		cfg := credentialCheckTestConfig(t, server.URL)

		extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
		server.Close()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, present := extra["credential_type"]; present {
			t.Fatalf("extra must not set credential_type itself, got %v", extra)
		}
		if _, present := extra["credential_id"]; present {
			t.Fatalf("extra must not carry credential_id, got %v", extra)
		}
		if extra["credential_type_note"] != "no endpoint names the type or the public id of the credential in use" {
			t.Fatalf("credential_type_note = %v", extra["credential_type_note"])
		}

		report := whoamiReport(cfg, auth.CredentialStoredKey, nil)
		for k, v := range extra {
			report[k] = v
		}
		if report["credential_type"] != "unknown" {
			t.Fatalf("merged credential_type = %v, want unknown", report["credential_type"])
		}
		if _, present := report["credential_id"]; present {
			t.Fatalf("merged report must not carry credential_id, got %v", report["credential_id"])
		}
	}
}

// T4: a "credential" value this CLI refuses to print still leaves the
// credential accepted: the server judged the credential, so a value it
// cannot safely be printed never turns accepted into another state.
func TestCheckSecretCredentialRefusedReferenceStaysAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, `{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}],`+
			`"credential":{"credentialType":"personal_pat","credentialId":"stored-secret"}}`)
	}))
	defer server.Close()
	cfg := credentialCheckTestConfig(t, server.URL)

	extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extra["verified"] != true || extra["verified_reason"] != verifiedAccepted {
		t.Fatalf("extra = %v, want verified=true/accepted even with a refused credential value", extra)
	}
	if _, present := extra["credential_type"]; present {
		t.Fatalf("a refused credential value must not set credential_type, got %v", extra["credential_type"])
	}
	note, _ := extra["credential_type_note"].(string)
	if note == "" || strings.Contains(note, "stored-secret") {
		t.Fatalf("credential_type_note = %q, want a note that never repeats the refused value", note)
	}
}

// Criterion 2/3: accounts_omitted is reported, as a distinct JSON field
// and a plain human sentence, only when conductor's own accountsOmitted
// field is present; a genuinely empty list with no accountsOmitted field
// is reported exactly as before this story.
func TestCheckSecretCredentialReportsAccountsOmitted(t *testing.T) {
	t.Run("typed credential: accounts omitted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, `{"accounts":[],"accountsOmitted":"typed_credential",`+
				`"credential":{"credentialType":"personal_pat","credentialId":"ckb1a2b3c4d5"}}`)
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
		if !ok || len(accounts) != 0 {
			t.Fatalf("accounts = %v, want an empty list", extra["accounts"])
		}
		if extra["accounts_omitted"] != "typed_credential" {
			t.Fatalf("accounts_omitted = %v, want %q", extra["accounts_omitted"], "typed_credential")
		}
		note, _ := extra["accounts_note"].(string)
		if !strings.Contains(note, "not shown for this credential type") {
			t.Fatalf("accounts_note = %q, want one plain sentence saying the account list is not shown for this credential type", note)
		}
	})

	t.Run("a genuinely empty list with no accountsOmitted is unchanged", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			respondJSON(w, http.StatusOK, `{"accounts":[]}`)
		}))
		defer server.Close()
		cfg := credentialCheckTestConfig(t, server.URL)

		extra, err := checkSecretCredential(cfg, auth.CredentialStoredKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, present := extra["accounts_omitted"]; present {
			t.Fatalf("accounts_omitted must not appear for a real empty list, got %v", extra["accounts_omitted"])
		}
		if _, present := extra["accounts_note"]; present {
			t.Fatalf("accounts_note must not appear for a real empty list, got %v", extra["accounts_note"])
		}
	})
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
	cases := []struct {
		name string
		body string
	}{
		{"no credential field", `{"accounts":[{"aid":"acct1","accountRole":"limited","isDefault":true}]}`},
		// T5: a hostile "credential" value whose id IS the stored secret must never reach the
		// report, nested inside credential_id or anywhere else: this is the row that is red the
		// moment the "credential" field is decoded without verifiedCredentialReference's guard.
		{"hostile credential field names the stored secret as its id", `{"accounts":[],"accountsOmitted":"typed_credential",` +
			`"credential":{"credentialType":"personal_pat","credentialId":"pat-secret-value"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respondJSON(w, http.StatusOK, tc.body)
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
			// The scan walks every string in the report, including nested values (the "accounts"
			// slice), by round-tripping through JSON rather than inspecting only top-level values.
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatalf("marshal report: %v", err)
			}
			if strings.Contains(string(data), "secret") {
				t.Fatalf("report carries a secret: %s", data)
			}
		})
	}
}

// T9 (partial): the exit codes and the three states are documented.
func TestWhoamiHelpDocumentsExitCodes(t *testing.T) {
	for _, want := range []string{
		verifiedAccepted, verifiedHumanSignInRequired, verifiedUnreachable, "exit 0", "exit 1",
		"credential_type", "credential_id",
	} {
		if !strings.Contains(whoamiCmd.Long, want) {
			t.Errorf("whoami help omits %q:\n%s", want, whoamiCmd.Long)
		}
	}
}
