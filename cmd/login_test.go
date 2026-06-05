package cmd

import (
	"strings"
	"testing"
)

// resolveLoginAccountID is the pure account-id picker for
// `runos login --api-key`: flag wins, else the existing config value,
// else an error; the result is charset-validated before it can be
// joined into request paths.
func TestResolveLoginAccountID(t *testing.T) {
	cases := []struct {
		name      string
		flag      string
		existing  string
		want      string
		wantError string // substring; "" means no error
	}{
		{"flag wins over existing", "acctxx", "older", "acctxx", ""},
		{"falls back to existing", "", "acctxx", "acctxx", ""},
		{"flag trims whitespace", "  acctxx\n", "", "acctxx", ""},
		{"existing trims whitespace", "", " acctxx ", "acctxx", ""},
		{"neither set errors", "", "", "", "requires an account id"},
		{"whitespace-only errors", "   ", "", "", "requires an account id"},
		{"slash rejected (path injection)", "ac/../x", "", "", "not a valid shape"},
		{"empty after charset (symbol only)", "!!", "", "", "not a valid shape"},
		{"too long rejected", strings.Repeat("a", 65), "", "", "not a valid shape"},
		{"max length accepted", strings.Repeat("a", 64), "", strings.Repeat("a", 64), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLoginAccountID(tc.flag, tc.existing)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result %q)", tc.wantError, got)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveLoginAccountID(%q, %q) = %q, want %q", tc.flag, tc.existing, got, tc.want)
			}
		})
	}
}
