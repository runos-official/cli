package cmd

import (
	"strings"
	"testing"
)

// resolveLoginAccountID is the pure account-id picker for
// `runos login --api-key`: --account-id is mandatory (no fallback to a
// stale config value, which would store an account-scoped PAT against
// the wrong tenant — bug 88), and the result is charset-validated before
// it can be joined into request paths.
func TestResolveLoginAccountID(t *testing.T) {
	cases := []struct {
		name      string
		flag      string
		want      string
		wantError string // substring; "" means no error
	}{
		{"flag accepted", "acctxx", "acctxx", ""},
		{"flag trims whitespace", "  acctxx\n", "acctxx", ""},
		{"absent flag errors (no config fallback)", "", "", "requires --account-id"},
		{"whitespace-only errors", "   ", "", "requires --account-id"},
		{"slash rejected (path injection)", "ac/../x", "", "not a valid shape"},
		{"empty after charset (symbol only)", "!!", "", "not a valid shape"},
		{"too long rejected", strings.Repeat("a", 65), "", "not a valid shape"},
		{"max length accepted", strings.Repeat("a", 64), strings.Repeat("a", 64), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLoginAccountID(tc.flag)
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
				t.Errorf("resolveLoginAccountID(%q) = %q, want %q", tc.flag, got, tc.want)
			}
		})
	}
}
