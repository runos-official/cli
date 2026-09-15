package config

import "testing"

func TestLeaveAccount(t *testing.T) {
	const t1, t2, t3 = "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"
	firebase := &FirebaseConfig{APIKey: "fb"}
	// signedIn is a login with a session on aaaaa that is also stored for bbbbb, plus an API key
	// on kkkkk and another login's sign-in on ppppp.
	signedIn := func() *Config {
		cfg := &Config{}
		cfg.ApplySessionLogin("ppppp", firebase, "other-login", t1)
		cfg.ApplyAPIKeyLogin("kkkkk", "pat-k", t1)
		cfg.ApplySessionLogin("aaaaa", firebase, "session", t2)
		cfg.ShareSessionWithAccounts([]string{"bbbbb"}, firebase, "session", t2)
		cfg.DefaultClusterID = "cluster1"
		return cfg
	}
	cases := []struct {
		name          string
		left, def     string
		want          LeaveOutcome
		wantActive    string
		wantCluster   string
		wantStored    map[string]bool
		wantActiveKey string
	}{
		{"leaving a non-active account keeps the active one", "bbbbb", "aaaaa", LeaveKeptActive, "aaaaa", "cluster1",
			map[string]bool{"aaaaa": true, "bbbbb": false, "kkkkk": true, "ppppp": true}, "session"},
		{"leaving the active account moves the session to the default account", "aaaaa", "bbbbb", LeaveMovedToDefault, "bbbbb", "",
			map[string]bool{"aaaaa": false, "bbbbb": true, "kkkkk": true, "ppppp": true}, "session"},
		{"a default account the CLI never stored is added", "aaaaa", "ccccc", LeaveMovedToDefault, "ccccc", "",
			map[string]bool{"aaaaa": false, "ccccc": true, "kkkkk": true, "ppppp": true}, "session"},
		{"no default account signs out of this login only", "aaaaa", "", LeaveSignedOut, "", "",
			map[string]bool{"aaaaa": false, "bbbbb": false, "kkkkk": true, "ppppp": true}, ""},
		{"a default account equal to the left one is no default", "aaaaa", "aaaaa", LeaveSignedOut, "", "",
			map[string]bool{"aaaaa": false, "bbbbb": false, "kkkkk": true, "ppppp": true}, ""},
		{"leaving the account an API key opens keeps the key", "kkkkk", "aaaaa", LeaveKeptActive, "aaaaa", "cluster1",
			map[string]bool{"aaaaa": true, "kkkkk": true}, "session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := signedIn()
			if got := cfg.LeaveAccount(tc.left, tc.def, t3); got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}
			if cfg.AccountID != tc.wantActive || cfg.RefreshToken != tc.wantActiveKey || cfg.DefaultClusterID != tc.wantCluster {
				t.Fatalf("active = %q token %q cluster %q", cfg.AccountID, cfg.RefreshToken, cfg.DefaultClusterID)
			}
			for accountID, want := range tc.wantStored {
				if got := cfg.HasStoredCredential(accountID); got != want {
					t.Errorf("stored credential for %s = %v, want %v", accountID, got, want)
				}
			}
			for _, account := range cfg.KnownAccounts {
				if account.Active != (account.AccountID == tc.wantActive) {
					t.Errorf("%s active = %v", account.AccountID, account.Active)
				}
			}
		})
	}
}
