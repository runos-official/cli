package config

import "testing"

/*
A default cluster belongs to ONE account, so it cannot survive a change of account.

MEASURED 2026-08-31 on a live machine. An operator signed in to a second account and every command
that did not carry `--cid` then failed:

	$ runos nodes list
	Error: Cluster <cid> not found in account <aid> (HTTP 404)

The cid it named was the FIRST account's default. Cluster ids are scoped to an account, so carrying
one across a sign-in produces a default that is guaranteed wrong rather than merely stale. The remedy is not a
better error message; it is not to keep it.

Re-authenticating the SAME account is not a change, and people do that to refresh a sign-in. Losing
their default for it would be a small daily annoyance with nothing gained.
*/

func TestSigningInToAnotherAccountDropsTheDefaultCluster(t *testing.T) {
	cfg := &Config{AccountID: "aaaaa", DefaultClusterID: "ccc"}

	cfg.ApplySessionLogin("bbbbb", &FirebaseConfig{APIKey: "k"}, "refresh", "2026-08-31T00:00:00Z")

	if cfg.DefaultClusterID != "" {
		t.Errorf("a default cluster from another account must not survive, got %q", cfg.DefaultClusterID)
	}
}

func TestReAuthenticatingTheSameAccountKeepsTheDefaultCluster(t *testing.T) {
	cfg := &Config{AccountID: "aaaaa", DefaultClusterID: "ccc"}

	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "k"}, "refresh", "2026-08-31T00:00:00Z")

	if cfg.DefaultClusterID != "ccc" {
		t.Errorf("the same account keeps its default, got %q", cfg.DefaultClusterID)
	}
}

// The first sign-in on a machine has no previous account, so there is nothing to contradict and
// nothing to clear.
func TestAFirstSignInLeavesAnyExistingDefaultAlone(t *testing.T) {
	cfg := &Config{DefaultClusterID: "ccc"}

	cfg.ApplySessionLogin("aaaaa", &FirebaseConfig{APIKey: "k"}, "refresh", "2026-08-31T00:00:00Z")

	if cfg.DefaultClusterID != "ccc" {
		t.Errorf("nothing contradicted it, got %q", cfg.DefaultClusterID)
	}
}

/*
A PAT is account-scoped too, and `login --api-key` names the account explicitly, so the same rule
applies to it.
*/
func TestStoringAPATForAnotherAccountDropsTheDefaultCluster(t *testing.T) {
	cfg := &Config{AccountID: "aaaaa", DefaultClusterID: "ccc"}

	cfg.ApplyAPIKeyLogin("bbbbb", "pat-value", "2026-08-31T00:00:00Z")

	if cfg.DefaultClusterID != "" {
		t.Errorf("a default cluster from another account must not survive, got %q", cfg.DefaultClusterID)
	}
	if cfg.APIKey != "pat-value" || cfg.AccountID != "bbbbb" {
		t.Errorf("the credential itself must still be stored, got %+v", cfg)
	}
	if cfg.RefreshToken != "" || cfg.Firebase != nil {
		t.Error("exactly one credential may be live at a time")
	}
}

/*
Signing out clears the default as well.

It is account state, and after a logout there is no account for it to belong to. Leaving it means
`runos status` keeps reporting a cluster on a machine that is signed out of everything.
*/
func TestSigningOutClearsTheDefaultCluster(t *testing.T) {
	cfg := &Config{
		AccountID:        "aaaaa",
		DefaultClusterID: "ccc",
		RefreshToken:     "refresh",
		Firebase:         &FirebaseConfig{APIKey: "k"},
		ConductorURL:     "https://api.example.invalid",
	}

	cfg.ClearSession()

	if cfg.DefaultClusterID != "" {
		t.Errorf("default cluster must go with the account, got %q", cfg.DefaultClusterID)
	}
	if cfg.RefreshToken != "" || cfg.Firebase != nil || cfg.AccountID != "" || cfg.APIKey != "" {
		t.Errorf("every credential must be cleared, got %+v", cfg)
	}
	// The URLs are NOT account state. Clearing them would silently move a signed-out machine back
	// to the default environment, which is not what signing out means.
	if cfg.ConductorURL != "https://api.example.invalid" {
		t.Errorf("the environment must survive a logout, got %q", cfg.ConductorURL)
	}
}
