package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/runos-official/cli/internal/auth"
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

	vpnResult := synchronizeVPNAccount(cmd, cfg, previousAccountID)
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

func synchronizeVPNAccount(cmd *cobra.Command, cfg *config.Config, previousAccountID string) vpnSynchronization {
	accountID := cfg.GetAccountID()
	result := vpnSynchronization{AccountID: accountID}
	daemon := vpnSocketClient(cmd)
	identityResponse, err := daemon.Call(vpn.Request{Op: vpn.OpIdentity, AccountID: accountID})
	if err != nil {
		var notRunning *vpn.NotRunningError
		if errors.As(err, &notRunning) {
			if previousAccountID != "" && previousAccountID != accountID {
				result.State = "mismatch"
				result.Message = "The CLI account changed, but the VPN daemon could not stop the old account. Run 'sudo runos vpn restart'."
			} else {
				result.State = "not-running"
				result.Message = "The account changed. The VPN service is not running."
			}
			return result
		}
		result.State = "failed"
		result.Message = err.Error()
		return result
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		result.State, result.Message = "failed", err.Error()
		return result
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "this-machine"
	}
	device, err := enrolDevice(cfg, token, identityResponse.Identity.PublicKey, hostname, runtime.GOOS)
	if errors.Is(err, errKeyRevoked) {
		rotated, rotateErr := daemon.Call(vpn.Request{Op: vpn.OpRotateKey, AccountID: accountID})
		if rotateErr != nil {
			result.State, result.Message = "failed", rotateErr.Error()
			return result
		}
		device, err = enrolDevice(cfg, token, rotated.Identity.PublicKey, hostname, runtime.GOOS)
	}
	if err != nil {
		result.State, result.Message = "failed", err.Error()
		return result
	}
	session, signInRequired, err := mintSession(cfg, token, device.ID)
	if err != nil {
		result.State, result.Message = "failed", err.Error()
		return result
	}
	if signInRequired {
		result.State = "failed"
		result.Message = "The VPN rejected the fresh browser authentication. Run 'runos vpn up' to retry."
		return result
	}
	response, err := daemon.Call(vpn.Request{Op: vpn.OpUp, AccountID: accountID, DeviceID: device.ID, ConductorURL: cfg.GetAPIURL(), SessionToken: session.Token, SessionExpiresAt: session.ExpiresAt})
	if err != nil {
		result.State, result.Message = "failed", err.Error()
		return result
	}
	result.Synchronized = true
	result.State = "synchronized"
	if response.Status != nil && !response.Status.Running {
		result.State = "down"
		result.Message = "The VPN account synchronized, but the tunnel is down."
	}
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
