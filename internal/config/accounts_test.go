package config

import "testing"

func TestRememberAccountKeepsOnlyOneActiveAccount(t *testing.T) {
	cfg := &Config{}
	cfg.RememberAccount("first", "2026-01-01T00:00:00Z")
	cfg.RememberAccount("second", "2026-01-02T00:00:00Z")
	cfg.RememberAccount("first", "2026-01-03T00:00:00Z")

	if len(cfg.KnownAccounts) != 2 {
		t.Fatalf("known accounts = %d, want 2", len(cfg.KnownAccounts))
	}
	if !cfg.KnownAccounts[0].Active || cfg.KnownAccounts[1].Active {
		t.Fatalf("active accounts = %+v", cfg.KnownAccounts)
	}
	if cfg.KnownAccounts[0].AddedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("first added time changed: %s", cfg.KnownAccounts[0].AddedAt)
	}
	if cfg.KnownAccounts[0].LastUsedAt != "2026-01-03T00:00:00Z" {
		t.Fatalf("first last-used time = %s", cfg.KnownAccounts[0].LastUsedAt)
	}
}

func TestForgetAccountDoesNotChangeOtherAccounts(t *testing.T) {
	cfg := &Config{KnownAccounts: []KnownAccount{{AccountID: "first"}, {AccountID: "second", Active: true}}}
	if !cfg.ForgetAccount("first") {
		t.Fatal("ForgetAccount returned false")
	}
	if len(cfg.KnownAccounts) != 1 || cfg.KnownAccounts[0].AccountID != "second" || !cfg.KnownAccounts[0].Active {
		t.Fatalf("known accounts = %+v", cfg.KnownAccounts)
	}
}
