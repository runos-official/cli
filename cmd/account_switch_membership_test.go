package cmd

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

/*
One login, several accounts, against the fake conductor and the fake Google.

Conductor authorises a signed-in request by membership, so one refresh token opens every account
its login is a member of. These tests hold the CLI to three things: a sign-in stores the session for
every member account, a switch to a member account needs no browser, and a stored credential whose
login left the account does not switch.
*/

// refusingFirebase makes every token refresh fail, which is the dead-credential path.
func refusingFirebase(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusBadRequest, `{"error":{"message":"TOKEN_EXPIRED"}}`)
	}))
	t.Cleanup(server.Close)
	auth.SetEndpointsForTest(t, server.URL+"/signin", server.URL+"/token")
}

// membershipTestConfig is an in-memory config pointed at the fake conductor, under a temp HOME so a
// Save lands nowhere real.
func membershipTestConfig(t *testing.T, apiURL string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	return &config.Config{ConductorURL: apiURL}
}

// stubBrowserSignIn runs the real device-code flow against the fake conductor without opening a
// browser.
func stubBrowserSignIn(t *testing.T) {
	t.Helper()
	previous := authenticateInBrowser
	authenticateInBrowser = func(cfg *config.Config, _ io.Writer) (browserSession, error) {
		return browserAuthenticateReporting(cfg, textSignIn{out: io.Discard}, false)
	}
	t.Cleanup(func() { authenticateInBrowser = previous })
}

// savedAccounts maps each stored account id to its saved record.
func savedAccounts(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	list, _ := readConfig(t)["known_accounts"].([]any)
	for _, raw := range list {
		record, _ := raw.(map[string]any)
		id, _ := record["account_id"].(string)
		out[id] = record
	}
	return out
}

func TestAStoredSignInForAMemberAccountSwitchesWithoutABrowser(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	conductor.memberships = []string{"aaaaa", "bbbbb"}
	cfg := membershipTestConfig(t, conductor.server.URL)
	cfg.ApplySessionLogin("bbbbb", &config.FirebaseConfig{APIKey: "KEY-B", ProjectID: "project-b"}, "REFRESH-B", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "KEY-A", ProjectID: "project-a"}, "REFRESH-A", "2026-01-02T00:00:00Z")

	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "bbbbb", "aaaaa")

	if !switched || err != nil {
		t.Fatalf("expected a switch, got switched=%v err=%v", switched, err)
	}
	if cfg.AccountID != "bbbbb" || cfg.RefreshToken != "REFRESH-B" {
		t.Fatalf("active = %q with token %q", cfg.AccountID, cfg.RefreshToken)
	}
	if cfg.Firebase == nil || cfg.Firebase.ProjectID != "project-b" {
		t.Fatalf("the switch kept the previous account's Firebase project: %+v", cfg.Firebase)
	}
	assertHasCall(t, conductor.recorded(), "user-accounts")
	assertNoCall(t, conductor.recorded(), "device-auth")
}

/*
A refresh token belongs to the login, not to the account, so a login removed from an account keeps
a token that still refreshes. The switch used to report success, and the next command was refused.
*/
func TestAStoredSignInWhoseLoginLeftTheAccountDoesNotSwitch(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	conductor.memberships = []string{"aaaaa"}
	cfg := membershipTestConfig(t, conductor.server.URL)
	cfg.ApplySessionLogin("bbbbb", &config.FirebaseConfig{APIKey: "KEY"}, "REFRESH-B", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "KEY"}, "REFRESH-A", "2026-01-02T00:00:00Z")

	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "bbbbb", "aaaaa")

	if !switched || err == nil || !strings.Contains(err.Error(), "no longer a member") {
		t.Fatalf("expected a clear refusal, got switched=%v err=%v", switched, err)
	}
	if !strings.Contains(err.Error(), "runos account add") {
		t.Errorf("the refusal must name the way forward, got %q", err)
	}
	if cfg.AccountID != "aaaaa" || cfg.RefreshToken != "REFRESH-A" {
		t.Fatalf("the refused switch changed the active account to %q (token %q)", cfg.AccountID, cfg.RefreshToken)
	}
	for _, account := range cfg.KnownAccounts {
		if account.Active != (account.AccountID == "aaaaa") {
			t.Errorf("the refused switch left %s with active=%v", account.AccountID, account.Active)
		}
	}
	if _, statErr := os.Stat(filepath.Join(os.Getenv("HOME"), ".runos", "config.json")); !os.IsNotExist(statErr) {
		t.Errorf("the refused switch wrote the config: %v", statErr)
	}
}

// An unreadable list is not a refusal. Conductor still refuses a non-member on every request, and a
// conductor without the list has nothing to read.
func TestAnUnreadableMembershipListDoesNotBlockTheSwitch(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	conductor.membershipStatus = http.StatusNotFound
	cfg := membershipTestConfig(t, conductor.server.URL)
	cfg.ApplySessionLogin("bbbbb", &config.FirebaseConfig{APIKey: "KEY"}, "REFRESH-B", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "KEY"}, "REFRESH-A", "2026-01-02T00:00:00Z")
	command := switchTestCommand()

	switched, err := switchWithStoredCredential(command, cfg, "bbbbb", "aaaaa")

	if !switched || err != nil {
		t.Fatalf("expected a switch, got switched=%v err=%v", switched, err)
	}
	if cfg.AccountID != "bbbbb" {
		t.Fatalf("active = %q, want bbbbb", cfg.AccountID)
	}
	if stderr := command.ErrOrStderr().(*bytes.Buffer).String(); !strings.Contains(stderr, "Could not confirm membership of bbbbb") {
		t.Errorf("the unconfirmed switch must say so, got %q", stderr)
	}
}

/*
An invite accepted after the sign-in is a membership nothing was stored for. Switching to it opened a
browser only to land on the same login.
*/
func TestAnAccountJoinedAfterSignInUsesTheCurrentSignIn(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	conductor.memberships = []string{"aaaaa", "ccccc"}
	cfg := membershipTestConfig(t, conductor.server.URL)
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "KEY", ProjectID: "proj"}, "REFRESH", "2026-01-01T00:00:00Z")
	command := switchTestCommand()

	switched, err := switchWithStoredCredential(command, cfg, "ccccc", "aaaaa")

	if !switched || err != nil {
		t.Fatalf("expected a switch, got switched=%v err=%v", switched, err)
	}
	if cfg.AccountID != "ccccc" || cfg.RefreshToken != "REFRESH" {
		t.Fatalf("active = %q with token %q", cfg.AccountID, cfg.RefreshToken)
	}
	assertNoCall(t, conductor.recorded(), "device-auth")
	// The membership was confirmed once, while deciding to share the session. A second read would
	// spend a round trip on an answer already in hand.
	if got := countCalls(conductor.recorded(), "user-accounts"); got != 1 {
		t.Errorf("read the membership list %d times, want 1", got)
	}
	if out := command.OutOrStdout().(*bytes.Buffer).String(); !strings.Contains(out, "current sign-in") {
		t.Errorf("the switch must say which credential it used, got %q", out)
	}
	if saved := savedAccounts(t)["ccccc"]; saved["refresh_token"] != "REFRESH" {
		t.Errorf("the joined account's credential was not saved: %v", saved)
	}
}

// A non-member with nothing stored still gets the browser: a sign-in with another login may reach
// it.
func TestANonMemberWithNothingStoredStillOffersTheBrowser(t *testing.T) {
	conductor := newFakeConductor(t)
	fakeFirebase(t)
	conductor.memberships = []string{"aaaaa"}
	cfg := membershipTestConfig(t, conductor.server.URL)
	cfg.ApplySessionLogin("aaaaa", &config.FirebaseConfig{APIKey: "KEY"}, "REFRESH", "2026-01-01T00:00:00Z")

	switched, err := switchWithStoredCredential(switchTestCommand(), cfg, "zzzzz", "aaaaa")

	if switched || err != nil {
		t.Fatalf("expected a deferral to the browser, got switched=%v err=%v", switched, err)
	}
	if cfg.AccountID != "aaaaa" || cfg.HasStoredCredential("zzzzz") {
		t.Fatalf("the deferral changed the config: active %q, zzzzz stored %v", cfg.AccountID, cfg.HasStoredCredential("zzzzz"))
	}
}

/*
The sign-in lands on the login's default account. `account switch bbbbb` used to fail with "does not
match" even though the login was a member of bbbbb.
*/
func TestABrowserSignInOnTheDefaultAccountOpensTheRequestedMemberAccount(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	stubBrowserSignIn(t)
	writeConfig(t, conductor.server.URL, "", false)
	conductor.signInAccount = "aaaaa"
	conductor.memberships = []string{"aaaaa", "bbbbb", "ccccc"}
	command := switchTestCommand()
	_ = command.Flags().Set("socket", daemon.path)

	if err := authenticateAndSwitchAccount(command, "bbbbb"); err != nil {
		t.Fatalf("switch: %v", err)
	}

	if got := readConfig(t)["account_id"]; got != "bbbbb" {
		t.Fatalf("active account = %v, want the requested bbbbb", got)
	}
	saved := savedAccounts(t)
	for _, id := range []string{"aaaaa", "bbbbb", "ccccc"} {
		if saved[id]["refresh_token"] != "REFRESH" {
			t.Errorf("%s has no saved credential after the sign-in: %v", id, saved[id])
		}
	}
}

func TestABrowserSignInRefusesAnAccountTheLoginIsNotAMemberOf(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	stubBrowserSignIn(t)
	writeConfig(t, conductor.server.URL, "", false)
	conductor.signInAccount = "aaaaa"
	conductor.memberships = []string{"aaaaa"}
	command := switchTestCommand()
	_ = command.Flags().Set("socket", daemon.path)

	err := authenticateAndSwitchAccount(command, "zzzzz")

	if err == nil || !strings.Contains(err.Error(), "not a member of account \"zzzzz\"") {
		t.Fatalf("expected a membership refusal, got %v", err)
	}
	if got := readConfig(t)["account_id"]; got != "" {
		t.Fatalf("the refused switch saved account %v", got)
	}
}

// `runos login` stores the session for every account the login is a member of, so the next switch
// to any of them needs no browser.
func TestLoginStoresTheSessionForEveryMemberAccount(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "", false)
	conductor.signInAccount = "aaaaa"
	conductor.memberships = []string{"aaaaa", "bbbbb"}

	if err := runLogin(newLoginTestCommand(daemon.path), nil); err != nil {
		t.Fatalf("login: %v", err)
	}

	if got := readConfig(t)["account_id"]; got != "aaaaa" {
		t.Fatalf("login must land on the sign-in's account, got %v", got)
	}
	saved := savedAccounts(t)
	for _, id := range []string{"aaaaa", "bbbbb"} {
		firebase, _ := saved[id]["firebase"].(map[string]any)
		if saved[id]["refresh_token"] != "REFRESH" || firebase["api_key"] != "KEY" {
			t.Errorf("%s was not stored with the session and its project: %v", id, saved[id])
		}
	}
	if saved["bbbbb"]["active"] == true {
		t.Error("a member account was marked active by the sign-in")
	}
}
