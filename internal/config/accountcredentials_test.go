package config

import "testing"

/*
Switching account must not sign you out of the one you left.

The config held ONE refresh token, so switching overwrote it: moving to a second
account signed you out of the first, and every switch back meant another browser
round-trip even seconds later with a token that had an hour left on it. Reported
by an operator moving between a provider account and a tenant account, which
is a thing this product asks people to do.
*/

func TestSwitchingBackKeepsTheFirstAccountSignedIn(t *testing.T) {
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "fb"}, "token-b", "2026-01-02T00:00:00Z")

	if !cfg.HasStoredCredential("aaaaa") {
		t.Fatal("the first account's credential was thrown away by switching")
	}
	if !cfg.ActivateStoredAccount("aaaaa", "2026-01-03T00:00:00Z") {
		t.Fatal("switching back should not need a sign-in")
	}
	if cfg.AccountID != "aaaaa" || cfg.RefreshToken != "token-a" {
		t.Fatalf("wrong credential became active: %q %q", cfg.AccountID, cfg.RefreshToken)
	}
	// And the account just left is still remembered, so this works both ways.
	if !cfg.HasStoredCredential("bbbbb") {
		t.Fatal("switching away threw the second account's credential away")
	}
}

func TestActivateReportsFalseForAnAccountItCannotOpen(t *testing.T) {
	cfg := &Config{}
	cfg.RememberAccount("aaaaa", "2026-01-01T00:00:00Z")
	// Remembered, but never signed in: the caller has to use the browser.
	if cfg.ActivateStoredAccount("aaaaa", "2026-01-02T00:00:00Z") {
		t.Fatal("activated an account with no stored credential")
	}
	if cfg.ActivateStoredAccount("zzzzz", "2026-01-02T00:00:00Z") {
		t.Fatal("activated an account it has never heard of")
	}
}

func TestOneCredentialKindPerAccount(t *testing.T) {
	// A refresh token is a session and an API key is not. Holding both for one
	// account is the state the two Apply functions exist to prevent, and a
	// stored pair would resurrect it on the next switch.
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplyAPIKeyLogin("aaaaa", "pat-a", "2026-01-02T00:00:00Z")
	for _, account := range cfg.KnownAccounts {
		if account.AccountID != "aaaaa" {
			continue
		}
		if account.RefreshToken != "" {
			t.Fatal("an API key login left the refresh token stored")
		}
		if account.APIKey != "pat-a" {
			t.Fatalf("the API key was not stored: %q", account.APIKey)
		}
	}
}

func TestLogoutClearsEveryStoredCredential(t *testing.T) {
	// `logout` means signed out. Leaving a usable credential behind for an
	// account somebody switched away from would let `account switch` sign them
	// back in without asking.
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "fb"}, "token-b", "2026-01-02T00:00:00Z")

	cfg.ClearSession()

	if cfg.HasStoredCredential("aaaaa") || cfg.HasStoredCredential("bbbbb") {
		t.Fatal("logout left a credential behind")
	}
	// The accounts themselves stay listed: forgetting them is `account forget`.
	if len(cfg.KnownAccounts) != 2 {
		t.Fatalf("logout forgot the accounts too: %d left", len(cfg.KnownAccounts))
	}
}

func TestForgettingAnAccountTakesItsCredential(t *testing.T) {
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	if !cfg.ForgetAccount("aaaaa") {
		t.Fatal("forget reported nothing to do")
	}
	if cfg.HasStoredCredential("aaaaa") {
		t.Fatal("a forgotten account kept a usable credential")
	}
}

func TestActivatingADifferentAccountDropsTheDefaultCluster(t *testing.T) {
	// Cluster ids are scoped to an account, so one carried across a switch is
	// not stale, it is guaranteed wrong.
	cfg := &Config{DefaultClusterID: "abc"}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "fb"}, "token-b", "2026-01-02T00:00:00Z")
	cfg.DefaultClusterID = "xyz"

	cfg.ActivateStoredAccount("aaaaa", "2026-01-03T00:00:00Z")

	if cfg.DefaultClusterID != "" {
		t.Fatalf("carried a default cluster across accounts: %q", cfg.DefaultClusterID)
	}
}

/*
One login can be a member of several accounts, and the same refresh token opens all of them. A
sign-in used to store the token against only the account it named, so every other member account
cost a browser round trip.
*/
func TestASignInOpensEveryAccountTheLoginIsAMemberOf(t *testing.T) {
	cfg := &Config{}
	project := &FirebaseConfig{APIKey: "fb", ProjectID: "proj"}
	cfg.ApplySessionLogin("aaaaa", project, "token-a", "2026-01-01T00:00:00Z")

	cfg.ShareSessionWithAccounts([]string{"aaaaa", "bbbbb", "ccccc"}, project, "token-a", "2026-01-01T00:00:00Z")

	if cfg.AccountID != "aaaaa" {
		t.Fatalf("sharing a session changed the active account to %q", cfg.AccountID)
	}
	for _, account := range cfg.KnownAccounts {
		if account.AccountID != "aaaaa" && account.Active {
			t.Errorf("%s was marked active by sharing a session", account.AccountID)
		}
		if account.AccountID != "aaaaa" && account.LastUsedAt != "" {
			t.Errorf("%s claims a last use it never had: %q", account.AccountID, account.LastUsedAt)
		}
	}
	for _, accountID := range []string{"bbbbb", "ccccc"} {
		if !cfg.HasStoredCredential(accountID) {
			t.Fatalf("%s has no stored credential after the sign-in", accountID)
		}
	}
	if !cfg.ActivateStoredAccount("ccccc", "2026-01-02T00:00:00Z") {
		t.Fatal("switching to a member account should not need a sign-in")
	}
	if cfg.RefreshToken != "token-a" || cfg.Firebase == nil || cfg.Firebase.ProjectID != "proj" {
		t.Fatalf("wrong credential became active: token %q firebase %+v", cfg.RefreshToken, cfg.Firebase)
	}
}

// A personal access token is a deliberate per-account choice. A later browser sign-in must not
// replace it with a session, which would change which identity acts on that account.
func TestSharingASessionKeepsAStoredAPIKey(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyAPIKeyLogin("bbbbb", "pat-b", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-02T00:00:00Z")

	cfg.ShareSessionWithAccounts([]string{"aaaaa", "bbbbb"}, &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-02T00:00:00Z")

	for _, account := range cfg.KnownAccounts {
		if account.AccountID != "bbbbb" {
			continue
		}
		if account.APIKey != "pat-b" || account.RefreshToken != "" {
			t.Fatalf("the stored API key was replaced: key %q token %q", account.APIKey, account.RefreshToken)
		}
	}
}

func TestSharingNoSessionStoresNothing(t *testing.T) {
	cfg := &Config{}
	cfg.ShareSessionWithAccounts([]string{"aaaaa", ""}, &FirebaseConfig{APIKey: "fb"}, "", "2026-01-01T00:00:00Z")
	if len(cfg.KnownAccounts) != 0 {
		t.Fatalf("an empty refresh token added accounts: %+v", cfg.KnownAccounts)
	}
}

/*
A refresh token is only spendable against the Firebase project that issued it. A switch used to
restore the token and keep the previous account's project, so a token from one project was sent to
another.
*/
func TestSwitchingRestoresTheFirebaseProjectOfTheSession(t *testing.T) {
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "key-a", ProjectID: "project-a"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "key-b", ProjectID: "project-b"}, "token-b", "2026-01-02T00:00:00Z")

	if !cfg.ActivateStoredAccount("aaaaa", "2026-01-03T00:00:00Z") {
		t.Fatal("switch back failed")
	}
	if cfg.Firebase == nil || cfg.Firebase.APIKey != "key-a" || cfg.Firebase.ProjectID != "project-a" {
		t.Fatalf("switched to aaaaa with the wrong Firebase project: %+v", cfg.Firebase)
	}
}

// A record written before the project was stored has none. Keeping the active project is what every
// switch did before, and clearing it would turn a working switch into a browser sign-in.
func TestSwitchingToARecordWithNoStoredProjectKeepsTheActiveOne(t *testing.T) {
	cfg := &Config{
		AccountID:    "bbbbb",
		RefreshToken: "token-b",
		Firebase:     &FirebaseConfig{APIKey: "fb"},
		KnownAccounts: []KnownAccount{
			{AccountID: "aaaaa", AddedAt: "2026-01-01T00:00:00Z", RefreshToken: "token-a"},
			{AccountID: "bbbbb", AddedAt: "2026-01-02T00:00:00Z", RefreshToken: "token-b", Active: true},
		},
	}
	if !cfg.ActivateStoredAccount("aaaaa", "2026-01-03T00:00:00Z") {
		t.Fatal("switch failed")
	}
	if cfg.Firebase == nil || cfg.Firebase.APIKey != "fb" {
		t.Fatalf("the active project was dropped: %+v", cfg.Firebase)
	}
}

func TestLogoutClearsEveryStoredProject(t *testing.T) {
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ShareSessionWithAccounts([]string{"bbbbb"}, &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")

	cfg.ClearSession()

	for _, account := range cfg.KnownAccounts {
		if account.Firebase != nil {
			t.Errorf("%s kept a Firebase project after logout", account.AccountID)
		}
	}
}

// Restoring from a plain struct copy left the account list changed, because the copy shared the
// backing array that activation rewrites in place.
func TestCloneSharesNoAccountState(t *testing.T) {
	cfg := &Config{}
	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "fb"}, "token-a", "2026-01-01T00:00:00Z")
	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "fb"}, "token-b", "2026-01-02T00:00:00Z")

	before := cfg.Clone()
	cfg.ActivateStoredAccount("aaaaa", "2026-01-03T00:00:00Z")
	cfg.KnownAccounts[0].Firebase.APIKey = "changed"
	*cfg = before

	for _, account := range cfg.KnownAccounts {
		if account.AccountID == "bbbbb" && !account.Active {
			t.Error("restoring the clone left bbbbb inactive")
		}
		if account.AccountID == "aaaaa" && (account.Active || account.LastUsedAt != "2026-01-01T00:00:00Z") {
			t.Errorf("restoring the clone kept the activation of aaaaa: %+v", account)
		}
		if account.Firebase != nil && account.Firebase.APIKey != "fb" {
			t.Errorf("the clone shares a Firebase pointer with the original: %+v", account.Firebase)
		}
	}
}
