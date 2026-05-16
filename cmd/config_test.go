package cmd

import (
	"strings"
	"testing"
)

// Regression test for the config-key-name drift: `runos status` used
// to point users at a non-existent `default-cluster` key, and
// `config set` / `config get` printed disagreeing valid-key lists
// (`conductor-url` vs `api-url`). Centralising the lists in
// configSettableKeys / configGettableKeys prevents this from
// reoccurring as long as both error strings and any future help text
// build off these constants. The test guards three things:
//
//  1. Every entry in configSettableKeys is actually settable
//     (handled in runConfigSet's switch).
//  2. configGettableKeys is a superset of configSettableKeys plus the
//     read-only keys env and account-id.
//  3. The legacy `default-cluster` hint string is gone from
//     `runos status` so the user's natural copy-paste lands on a
//     real key.
func TestConfigKeyLists_AreConsistent(t *testing.T) {
	wantSettable := map[string]bool{"cid": true, "console-url": true, "api-url": true}
	if len(configSettableKeys) != len(wantSettable) {
		t.Fatalf("configSettableKeys size %d, want %d", len(configSettableKeys), len(wantSettable))
	}
	for _, k := range configSettableKeys {
		if !wantSettable[k] {
			t.Errorf("configSettableKeys lists unexpected key %q", k)
		}
	}

	wantGettable := []string{"env", "account-id", "cid", "console-url", "api-url"}
	if !equalStringSlice(configGettableKeys, wantGettable) {
		t.Errorf("configGettableKeys = %v, want %v", configGettableKeys, wantGettable)
	}
	for _, k := range configSettableKeys {
		found := false
		for _, g := range configGettableKeys {
			if g == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("settable key %q is missing from configGettableKeys", k)
		}
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// configKeyAliases must point every historical / snake_case spelling at
// a key that is itself accepted by runConfigSet (for settable aliases)
// or runConfigGet (for read-only aliases). A typo here would land the
// user back in the pre-fix "unknown config key" loop after an alias
// rewrite.
func TestConfigKeyAliases_TargetCanonicalKeys(t *testing.T) {
	gettable := map[string]bool{}
	for _, k := range configGettableKeys {
		gettable[k] = true
	}
	for alias, canonical := range configKeyAliases {
		if !gettable[canonical] {
			t.Errorf("alias %q targets %q which is not in configGettableKeys", alias, canonical)
		}
	}
}

// I18-J regression: `runos config set <key> <value>` validates per-key
// constraints so a typo or an empty string can't silently poison the
// config file. Pre-fix the setter accepted any string for any key,
// including `runos config set cid ""`, which broke every downstream
// command that fell back to the default cluster.
// Regression: `config set env beta` used to error "unknown config key:
// env" though env IS a valid config key, just not settable directly via
// `config set` (it's owned by `config env`). The misleading wording sent
// users digging for a typo. The error now distinguishes read-only keys
// from unknown keys and points at the right path.
func TestConfigSetUnknownKeyError(t *testing.T) {
	cases := []struct {
		name      string
		rawKey    string
		normKey   string
		wantSubs  []string
	}{
		{"env redirects to config env subcommand", "env", "env", []string{"read-only", "runos config env"}},
		{"account-id redirects to login", "account-id", "account-id", []string{"auth flow", "runos login"}},
		{"genuine typo still says unknown", "garbage", "garbage", []string{"unknown config key", "garbage"}},
		{"alias resolves to read-only target", "account_id", "account-id", []string{"auth flow", "runos login"}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := configSetUnknownKeyError(tt.rawKey, tt.normKey)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestValidateConfigSet(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		value     string
		wantErr   bool
		errSubstr string
	}{
		// cid charset gate
		{"cid empty rejected", "cid", "", true, "must not be empty"},
		{"cid whitespace rejected", "cid", "   ", true, "valid cluster id shape"},
		{"cid uppercase rejected", "cid", "I4Y", true, "valid cluster id shape"},
		{"cid hyphen rejected", "cid", "i-4y", true, "valid cluster id shape"},
		{"cid too short rejected", "cid", "ab", true, "valid cluster id shape"},
		{"cid too long rejected", "cid", "abcdefghijklmnopq", true, "valid cluster id shape"},
		{"cid 3-char lowercase alphanumeric passes", "cid", "mycluster2", false, ""},
		{"cid 16-char lowercase alphanumeric passes", "cid", "abcdefghij012345", false, ""},
		{"cid hint mentions clusters list on empty", "cid", "", true, "runos clusters list"},
		{"cid hint mentions clusters list on bad shape", "cid", "BAD", true, "runos clusters list"},

		// URL gates
		{"console-url empty rejected", "console-url", "", true, "must not be empty"},
		{"console-url scheme-only rejected", "console-url", "https://", true, "scheme and host"},
		{"console-url host-only rejected", "console-url", "example.com", true, "scheme and host"},
		{"console-url ftp scheme rejected", "console-url", "ftp://example.com", true, "http or https"},
		{"console-url https passes", "console-url", "https://console.runos.xyz", false, ""},
		{"console-url http passes (localhost dev)", "console-url", "http://localhost:5177", false, ""},

		{"api-url empty rejected", "api-url", "", true, "must not be empty"},
		{"api-url malformed scheme rejected", "api-url", "://no-scheme", true, "scheme and host"},
		{"api-url http passes (localhost dev)", "api-url", "http://localhost:3025", false, ""},
		{"api-url https passes", "api-url", "https://api.runos.xyz", false, ""},

		// Unknown key falls through (caller handles the error message)
		{"unknown key passes validate (caller handles unknown)", "garbage", "anything", false, ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfigSet(tt.key, tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateConfigSet(%q, %q): want error, got nil", tt.key, tt.value)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("error %q missing substring %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateConfigSet(%q, %q): unexpected error %v", tt.key, tt.value, err)
			}
		})
	}
}
