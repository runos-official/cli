package cmd

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"
	"github.com/spf13/cobra"
)

type accountListEntry struct {
	AccountID  string `json:"accountId"`
	Active     bool   `json:"active"`
	AddedAt    string `json:"addedAt"`
	LastUsedAt string `json:"lastUsedAt"`
	// Label, AccountRole and IsDefault come from conductor's membership list. They are empty when
	// the CLI is signed out, when the list could not be read, and for a local account the active
	// sign-in is not a member of.
	Label               string     `json:"label,omitempty"`
	AccountRole         string     `json:"accountRole,omitempty"`
	IsDefault           bool       `json:"isDefault,omitempty"`
	VPNIdentityPresent  bool       `json:"vpnIdentityPresent"`
	VPNSessionPresent   bool       `json:"vpnSessionPresent"`
	VPNSessionExpiresAt *time.Time `json:"vpnSessionExpiresAt,omitempty"`
}

type accountListResult struct {
	SchemaVersion int                `json:"schemaVersion"`
	Accounts      []accountListEntry `json:"accounts"`
}

func runAccountList(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	known := append([]config.KnownAccount(nil), cfg.KnownAccounts...)
	if cfg.AccountID != "" && !containsKnownAccount(known, cfg.AccountID) {
		known = append(known, config.KnownAccount{AccountID: cfg.AccountID, AddedAt: cfg.SignedInAt, LastUsedAt: cfg.SignedInAt, Active: true})
	}
	identities := map[string]vpn.Identity{}
	if response, callErr := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpIdentities}); callErr == nil {
		for _, identity := range response.Identities {
			identities[identity.AccountID] = identity
		}
	}
	memberships := signedInMemberships(cfg)
	if memberships != nil && auth.Kind(cfg) == auth.CredentialInteractive {
		known = dropAccountsTheLoginLeft(known, memberships, cfg.RefreshToken)
	}
	result := accountListResult{SchemaVersion: 1, Accounts: mergeAccountList(known, cfg.AccountID, memberships)}
	for i := range result.Accounts {
		identity, present := identities[result.Accounts[i].AccountID]
		result.Accounts[i].VPNIdentityPresent = present
		result.Accounts[i].VPNSessionPresent = identity.SessionPresent
		if !identity.SessionExpiresAt.IsZero() {
			expires := identity.SessionExpiresAt
			result.Accounts[i].VPNSessionExpiresAt = &expires
		}
	}
	return emitAccountResult(cmd, result, func() { printAccountList(cmd.OutOrStdout(), result.Accounts) })
}

/*
The accounts this machine knows, joined with the accounts the signed-in login is a member of.

Local accounts come first, in the order they were added. A membership the CLI has never stored comes
after them, in conductor's order, because the login can reach it and `account switch` opens it
without a browser. A local account that is not in the membership list is listed with no label.
runAccountList has already dropped the accounts the signed-in login left, so what remains belongs
to another login or to an API key.
*/
func mergeAccountList(known []config.KnownAccount, activeAccountID string, memberships []api.UserAccount) []accountListEntry {
	byID := make(map[string]api.UserAccount, len(memberships))
	for _, membership := range memberships {
		byID[membership.AID] = membership
	}
	listed := map[string]bool{}
	entries := make([]accountListEntry, 0, len(known)+len(memberships))
	for _, account := range known {
		entry := accountListEntry{
			AccountID: account.AccountID, Active: account.Active && activeAccountID == account.AccountID,
			AddedAt: account.AddedAt, LastUsedAt: account.LastUsedAt,
		}
		if membership, ok := byID[account.AccountID]; ok {
			entry.Label, entry.AccountRole, entry.IsDefault = membership.Label(), membership.AccountRole, membership.IsDefault
		}
		listed[account.AccountID] = true
		entries = append(entries, entry)
	}
	for _, membership := range memberships {
		if membership.AID == "" || listed[membership.AID] {
			continue
		}
		listed[membership.AID] = true
		entries = append(entries, accountListEntry{
			AccountID: membership.AID, Label: membership.Label(),
			AccountRole: membership.AccountRole, IsDefault: membership.IsDefault,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := entries[i].AddedAt, entries[j].AddedAt
		if (left == "") != (right == "") {
			return left != ""
		}
		return left < right
	})
	return entries
}

/*
dropAccountsTheLoginLeft removes the local accounts that the signed-in login is no longer a member of.

The login left them, from this CLI or from the console. Listing one offered a switch that conductor
refuses. Call it only with a membership list that was read for an interactive sign-in: nil means
unread, and an API key's list names only its own account.

The list speaks only for the active sign-in. So a local account stays when it holds an API key or
another login's sign-in. An account with nothing stored is dropped, because nothing on this machine
opens it.
*/
func dropAccountsTheLoginLeft(known []config.KnownAccount, memberships []api.UserAccount, session string) []config.KnownAccount {
	kept := make([]config.KnownAccount, 0, len(known))
	for _, account := range known {
		otherLogin := account.RefreshToken != "" && account.RefreshToken != session
		if hasMembership(memberships, account.AccountID) || account.APIKey != "" || otherLogin {
			kept = append(kept, account)
		}
	}
	return kept
}

// printAccountList writes one aligned line per account. Trailing padding is trimmed, so a
// signed-out list reads exactly as it did before the membership columns existed.
func printAccountList(out io.Writer, entries []accountListEntry) {
	var buffer bytes.Buffer
	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
	for _, entry := range entries {
		marker := " "
		if entry.Active {
			marker = "*"
		}
		note := ""
		if entry.IsDefault {
			note = "default"
		}
		fmt.Fprintf(writer, "%s %s\t%s\t%s\t%s\n", marker, entry.AccountID, entry.Label, entry.AccountRole, note)
	}
	_ = writer.Flush()
	for _, line := range strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
}

func containsKnownAccount(accounts []config.KnownAccount, accountID string) bool {
	for _, account := range accounts {
		if account.AccountID == accountID {
			return true
		}
	}
	return false
}
