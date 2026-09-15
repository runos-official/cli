package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
)

func TestMergeAccountList(t *testing.T) {
	known := []config.KnownAccount{
		{AccountID: "bbbbb", AddedAt: "2026-01-02T00:00:00Z"},
		{AccountID: "aaaaa", AddedAt: "2026-01-01T00:00:00Z", Active: true},
		{AccountID: "kkkkk", AddedAt: "2026-01-03T00:00:00Z"},
	}
	server := []api.UserAccount{
		{AID: "aaaaa", CompanyName: "Acme", Name: "first", AccountRole: "admin", IsDefault: true},
		{AID: "ccccc", Name: "Tenant", AccountRole: "limited"},
		{AID: "bbbbb", Name: "Second", AccountRole: "limited"},
	}

	entries := mergeAccountList(known, "aaaaa", server)

	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		order = append(order, entry.AccountID)
	}
	// Local accounts in the order they were added, then memberships the CLI never stored.
	if strings.Join(order, ",") != "aaaaa,bbbbb,kkkkk,ccccc" {
		t.Fatalf("order = %v", order)
	}
	first, second, local, serverOnly := entries[0], entries[1], entries[2], entries[3]
	if !first.Active || first.Label != "Acme" || first.AccountRole != "admin" || !first.IsDefault {
		t.Errorf("aaaaa = %+v", first)
	}
	if second.Active || second.Label != "Second" || second.AccountRole != "limited" || second.IsDefault {
		t.Errorf("bbbbb = %+v", second)
	}
	// A local account the sign-in is not a member of stays listed, undecorated: it can belong to
	// another login or to an API key.
	if local.Label != "" || local.AccountRole != "" {
		t.Errorf("kkkkk was decorated from a list that does not speak for it: %+v", local)
	}
	if serverOnly.Active || serverOnly.Label != "Tenant" || serverOnly.AccountRole != "limited" || serverOnly.AddedAt != "" {
		t.Errorf("ccccc = %+v", serverOnly)
	}
}

// The active flag needs BOTH the stored flag and the config's account, as before.
func TestMergeAccountListNeedsTheConfigToAgreeOnTheActiveAccount(t *testing.T) {
	entries := mergeAccountList([]config.KnownAccount{{AccountID: "aaaaa", Active: true}}, "bbbbb", nil)
	if entries[0].Active {
		t.Fatal("an account the config is not on was listed as active")
	}
}

func TestPrintAccountList(t *testing.T) {
	t.Run("signed out reads exactly as before", func(t *testing.T) {
		var out bytes.Buffer
		printAccountList(&out, []accountListEntry{{AccountID: "aaaaa", Active: true}, {AccountID: "bbbbb"}})
		if out.String() != "* aaaaa\n  bbbbb\n" {
			t.Fatalf("output = %q", out.String())
		}
	})
	t.Run("signed in shows the label, the role and the default", func(t *testing.T) {
		var out bytes.Buffer
		printAccountList(&out, []accountListEntry{
			{AccountID: "aaaaa", Active: true, Label: "Acme", AccountRole: "admin", IsDefault: true},
			{AccountID: "bbbbb", Label: "Tenant", AccountRole: "limited"},
		})
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("lines = %q", lines)
		}
		for _, want := range []string{"* aaaaa", "Acme", "admin", "default"} {
			if !strings.Contains(lines[0], want) {
				t.Errorf("line %q is missing %q", lines[0], want)
			}
		}
		if !strings.Contains(lines[1], "Tenant") || !strings.Contains(lines[1], "limited") || strings.Contains(lines[1], "default") {
			t.Errorf("line %q", lines[1])
		}
		for _, line := range lines {
			if strings.HasSuffix(line, " ") {
				t.Errorf("trailing padding on %q", line)
			}
		}
	})
}

/*
A login that left an account, from this CLI or from the console, kept seeing it in `account list`,
and switching to it was refused. When the signed-in login's membership list is read, it speaks for
the accounts that login's session opens.
*/
func TestAccountListDropsAnAccountTheSignedInLoginLeft(t *testing.T) {
	cases := []struct {
		name             string
		membershipStatus int
		want             []string
	}{
		{"a read list drops the left account", 0, []string{"aaaaa", "kkkkk", "ppppp"}},
		{"an unreadable list drops nothing", http.StatusInternalServerError, []string{"aaaaa", "bbbbb", "kkkkk", "nnnnn", "ppppp"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conductor := newFakeConductor(t)
			fakeFirebase(t)
			conductor.memberships = []string{"aaaaa"}
			conductor.membershipStatus = tc.membershipStatus
			cfg := membershipTestConfig(t, conductor.server.URL)
			firebase := &config.FirebaseConfig{APIKey: "KEY"}
			cfg.RememberAccount("nnnnn", "2026-01-01T00:00:00Z")
			cfg.ApplySessionLogin("ppppp", firebase, "OTHER-LOGIN", "2026-01-02T00:00:00Z")
			cfg.ApplyAPIKeyLogin("kkkkk", "pat-k", "2026-01-03T00:00:00Z")
			cfg.ApplySessionLogin("aaaaa", firebase, "REFRESH", "2026-01-04T00:00:00Z")
			cfg.ShareSessionWithAccounts([]string{"bbbbb"}, firebase, "REFRESH", "2026-01-04T00:00:00Z")
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}

			command := switchTestCommand()
			_ = command.Flags().Set("json", "true")
			_ = command.Flags().Set("socket", filepath.Join(t.TempDir(), "no-daemon.sock"))
			var out bytes.Buffer
			command.SetOut(&out)
			if err := runAccountList(command, nil); err != nil {
				t.Fatalf("account list: %v", err)
			}

			var result accountListResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("parse %q: %v", out.String(), err)
			}
			got := make([]string, 0, len(result.Accounts))
			for _, entry := range result.Accounts {
				got = append(got, entry.AccountID)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("listed %v, want %v", got, tc.want)
			}
		})
	}
}
