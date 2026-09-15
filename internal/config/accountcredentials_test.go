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
