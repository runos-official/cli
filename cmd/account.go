package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"
	"github.com/spf13/cobra"
)

var accountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage the active RunOS account",
}

var accountListCmd = &cobra.Command{Use: "list", Short: "List locally known accounts", RunE: runAccountList}
var accountAddCmd = &cobra.Command{Use: "add", Short: "Authenticate and add an account", RunE: runAccountAdd}
var accountSwitchCmd = &cobra.Command{Use: "switch <account-id>", Args: cobra.ExactArgs(1), Short: "Authenticate and switch accounts", RunE: runAccountSwitch}

func init() {
	// The same hidden escape hatch every other daemon-talking command has. Both of these change the
	// active identity, so both take the tunnel down with it (FPL26 D3).
	for _, c := range []*cobra.Command{accountAddCmd, accountSwitchCmd} {
		c.Flags().String("socket", "", "path to the daemon control socket (advanced)")
		_ = c.Flags().MarkHidden("socket")
	}
}

var accountForgetCmd = &cobra.Command{Use: "forget <account-id>", Args: cobra.ExactArgs(1), Short: "Forget local account data", RunE: runAccountForget}

func init() {
	for _, command := range []*cobra.Command{accountListCmd, accountAddCmd, accountSwitchCmd, accountForgetCmd} {
		command.Flags().BoolP("json", "j", false, "Output as JSON")
	}
	accountForgetCmd.Flags().Bool("yes", false, "Confirm removal of local account data")
	accountCmd.AddCommand(accountListCmd, accountAddCmd, accountSwitchCmd, accountForgetCmd)
}

type accountListEntry struct {
	AccountID           string     `json:"accountId"`
	Active              bool       `json:"active"`
	AddedAt             string     `json:"addedAt"`
	LastUsedAt          string     `json:"lastUsedAt"`
	VPNIdentityPresent  bool       `json:"vpnIdentityPresent"`
	VPNSessionPresent   bool       `json:"vpnSessionPresent"`
	VPNSessionExpiresAt *time.Time `json:"vpnSessionExpiresAt,omitempty"`
}

type accountListResult struct {
	SchemaVersion int                `json:"schemaVersion"`
	Accounts      []accountListEntry `json:"accounts"`
}

type vpnSynchronization struct {
	State        string `json:"state"`
	AccountID    string `json:"accountId,omitempty"`
	Synchronized bool   `json:"synchronized"`
	Message      string `json:"message,omitempty"`
}

type accountSwitchResult struct {
	SchemaVersion  int                `json:"schemaVersion"`
	AccountID      string             `json:"accountId"`
	AccountChanged bool               `json:"accountChanged"`
	VPN            vpnSynchronization `json:"vpn"`
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
	result := accountListResult{SchemaVersion: 1}
	for _, account := range known {
		identity, present := identities[account.AccountID]
		entry := accountListEntry{
			AccountID: account.AccountID, Active: account.Active && cfg.AccountID == account.AccountID,
			AddedAt: account.AddedAt, LastUsedAt: account.LastUsedAt, VPNIdentityPresent: present,
			VPNSessionPresent: identity.SessionPresent,
		}
		if !identity.SessionExpiresAt.IsZero() {
			expires := identity.SessionExpiresAt
			entry.VPNSessionExpiresAt = &expires
		}
		result.Accounts = append(result.Accounts, entry)
	}
	sort.SliceStable(result.Accounts, func(i, j int) bool { return result.Accounts[i].AddedAt < result.Accounts[j].AddedAt })
	return emitAccountResult(cmd, result, func() {
		for _, account := range result.Accounts {
			marker := " "
			if account.Active {
				marker = "*"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", marker, account.AccountID)
		}
	})
}

func containsKnownAccount(accounts []config.KnownAccount, accountID string) bool {
	for _, account := range accounts {
		if account.AccountID == accountID {
			return true
		}
	}
	return false
}

func runAccountAdd(cmd *cobra.Command, _ []string) error {
	return authenticateAndSwitchAccount(cmd, "")
}

func runAccountSwitch(cmd *cobra.Command, args []string) error {
	return authenticateAndSwitchAccount(cmd, args[0])
}

func authenticateAndSwitchAccount(cmd *cobra.Command, requestedAccountID string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	previousAccountID := cfg.AccountID
	progress := cmd.OutOrStdout()
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		progress = cmd.ErrOrStderr()
	}
	session, err := authenticateInBrowser(cfg, progress)
	if err != nil {
		return err
	}
	if err := verifyRequestedAccount(requestedAccountID, session.AccountID); err != nil {
		return err
	}
	commitBrowserSession(cfg, session)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save account context: %w", err)
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	vpnResult := disconnectVPNForAccountChange(socketPath, previousAccountID, cfg.GetAccountID())
	result := accountSwitchResult{SchemaVersion: 1, AccountID: session.AccountID, AccountChanged: previousAccountID != session.AccountID, VPN: vpnResult}
	return emitAccountResult(cmd, result, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Active account: %s\n", session.AccountID)
		if vpnResult.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), vpnResult.Message)
		}
	})
}

func verifyRequestedAccount(requested, authenticated string) error {
	if requested != "" && requested != authenticated {
		return fmt.Errorf("authenticated account %q does not match requested account %q", authenticated, requested)
	}
	return nil
}

/*
The states an account switch reports for the VPN. Named constants because a caller branches on
them; the message beside each is written for a person and is expected to be reworded.
*/
const (
	vpnStateDisconnected = "disconnected"
	vpnStateUnchanged    = "unchanged"
	vpnStateNotRunning   = "not-running"
	vpnStateFailed       = "failed"
)

/*
Take the tunnel down, because the identity that opened it has changed (FPL26 D3).

THIS USED TO CONNECT. It enrolled this machine under the new account, minted a session and called
`up`, unconditionally, without ever asking whether the tunnel had been running. So switching account
on a machine with the VPN deliberately off turned it on. The same complaint was reported against
RunOS Desktop, whose automatic account-follow did exactly this: "even though i don't have the
connect at startup option selected, i seem to be connected". The app's copy was removed; this one
was not.

It was also a SECOND COPY of the enrol-mint-up sequence that `vpn up` owns. Two copies of an
account-scoped sequence is how the account-switch defects got in: one of them was fixed and the
other was not. There is now one, and this is not it.

Connecting the new account is the person's decision, and it is one command.
*/
func disconnectVPNForAccountChange(socketPath, previousAccountID, accountID string) vpnSynchronization {
	result := vpnSynchronization{AccountID: accountID}

	// A re-authentication of the SAME account is not a change, and people do it to refresh a
	// sign-in. Dropping their tunnel for it would be an unpleasant surprise.
	if previousAccountID == accountID || previousAccountID == "" {
		result.State = vpnStateUnchanged
		return result
	}

	response, err := vpn.NewClient(socketPath).Call(vpn.Request{Op: vpn.OpDown})
	if err != nil {
		// No daemon is the ORDINARY case: `runos desktop install` does not write one. It must never
		// stop somebody changing account, and it is not an error worth reporting as one.
		var notRunning *vpn.NotRunningError
		if errors.As(err, &notRunning) {
			result.State = vpnStateNotRunning
			return result
		}
		result.State, result.Message = vpnStateFailed, err.Error()
		return result
	}
	_ = response

	result.State = vpnStateDisconnected
	result.Message = fmt.Sprintf(
		"The VPN was disconnected because the account changed. Run 'runos vpn up' to connect %s.",
		accountID,
	)
	return result
}

func runAccountForget(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		return fmt.Errorf("account forget requires --yes")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	accountID := args[0]
	wasActive := cfg.AccountID == accountID
	removed := cfg.ForgetAccount(accountID)
	if wasActive {
		cfg.RefreshToken = ""
		cfg.Firebase = nil
		cfg.AccountID = ""
		cfg.SignedInAt = ""
		cfg.APIKey = ""
		cfg.ClearActiveAccount()
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save account metadata: %w", err)
	}
	vpnRemoved := false
	if _, callErr := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpForgetIdentity, AccountID: accountID}); callErr == nil {
		vpnRemoved = true
	}
	result := struct {
		SchemaVersion      int    `json:"schemaVersion"`
		AccountID          string `json:"accountId"`
		Forgotten          bool   `json:"forgotten"`
		VPNIdentityRemoved bool   `json:"vpnIdentityRemoved"`
	}{1, accountID, removed, vpnRemoved}
	return emitAccountResult(cmd, result, func() { fmt.Fprintf(cmd.OutOrStdout(), "Forgot account %s.\n", accountID) })
}

func emitAccountResult(cmd *cobra.Command, result any, human func()) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if !jsonOutput {
		human()
		return nil
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}
