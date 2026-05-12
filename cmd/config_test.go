package cmd

import (
	"strings"
	"testing"
)

// I18-J regression: `runos config set <key> <value>` validates per-key
// constraints so a typo or an empty string can't silently poison the
// config file. Pre-fix the setter accepted any string for any key,
// including `runos config set cid ""`, which broke every downstream
// command that fell back to the default cluster.
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
