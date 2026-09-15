package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
)

func memberships(ids ...string) []api.UserAccount {
	accounts := make([]api.UserAccount, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, api.UserAccount{AID: id})
	}
	return accounts
}

/*
A sign-in lands on the login's DEFAULT account. `account switch` to another account the login is a
member of used to fail with "does not match", although conductor would serve the account.
*/
func TestVerifyRequestedAccount(t *testing.T) {
	cases := []struct {
		name          string
		requested     string
		authenticated string
		memberships   []api.UserAccount
		want          string
		wantError     string
	}{
		{"add takes the sign-in's account", "", "aaaaa", memberships("aaaaa", "bbbbb"), "aaaaa", ""},
		{"the sign-in's own account passes", "aaaaa", "aaaaa", nil, "aaaaa", ""},
		{"a member account passes and becomes active", "bbbbb", "aaaaa", memberships("aaaaa", "bbbbb"), "bbbbb", ""},
		{"a non-member account is refused and names the memberships", "zzzzz", "aaaaa", memberships("aaaaa", "bbbbb"), "", "it is a member of: aaaaa, bbbbb"},
		{"an unreadable list confirms only the sign-in's account", "bbbbb", "aaaaa", nil, "", "does not match"},
		{"an empty list is a real answer", "bbbbb", "aaaaa", memberships(), "", "or of any account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifyRequestedAccount(tc.requested, tc.authenticated, tc.memberships)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantError)
				}
				if got != "" {
					t.Fatalf("a refusal still named an account to activate: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("active account = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHasMembership(t *testing.T) {
	cases := []struct {
		name      string
		accounts  []api.UserAccount
		accountID string
		want      bool
	}{
		{"listed", memberships("aaaaa", "bbbbb"), "bbbbb", true},
		{"not listed", memberships("aaaaa"), "bbbbb", false},
		{"nil list", nil, "aaaaa", false},
		// A row with no id must not make an empty request look like a member.
		{"empty id never matches", []api.UserAccount{{AID: ""}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasMembership(tc.accounts, tc.accountID); got != tc.want {
				t.Fatalf("hasMembership = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMemberAccountIDsSkipsRowsWithNoID(t *testing.T) {
	got := memberAccountIDs([]api.UserAccount{{AID: "aaaaa"}, {AID: ""}, {AID: "bbbbb"}})
	if strings.Join(got, ",") != "aaaaa,bbbbb" {
		t.Fatalf("memberAccountIDs = %v", got)
	}
}
