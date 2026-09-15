package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
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
var accountSwitchCmd = &cobra.Command{Use: "switch <account-id>", Args: cobra.ExactArgs(1), Short: "Switch accounts, signing in only when the CLI has to", RunE: runAccountSwitch}

func init() {
	// The same hidden escape hatch every other daemon-talking command has. Both of these change the
	// active identity, so both take the tunnel down with it (FPL26 D3).
	registerSocketFlag(accountAddCmd, accountSwitchCmd, accountListCmd, accountForgetCmd)
}

var accountForgetCmd = &cobra.Command{Use: "forget <account-id>", Args: cobra.ExactArgs(1), Short: "Forget local account data", RunE: runAccountForget}

func init() {
	for _, command := range []*cobra.Command{accountListCmd, accountAddCmd, accountSwitchCmd, accountForgetCmd} {
		command.Flags().BoolP("json", "j", false, "Output as JSON")
	}
	accountForgetCmd.Flags().Bool("yes", false, "Confirm removal of local account data")
	accountCmd.AddCommand(accountListCmd, accountAddCmd, accountSwitchCmd, accountForgetCmd)
}

/*
What an account change did to the VPN.

`synchronized` is GONE, along with the `synchronized`/`down`/`mismatch` states it went with. They
described a step that no longer exists: switching account used to enrol, mint and connect, and now
takes the tunnel down and leaves it down. The field survived the rewrite as a stump that was never
assigned, so every result reported `"synchronized": false` whatever happened.

`schemaVersion` moves to 2 on `accountSwitchResult` because that is a breaking change to a payload,
and leaving it at 1 while the states change underneath is how a consumer silently stops matching.
*/
type vpnSynchronization struct {
	State     string `json:"state"`
	AccountID string `json:"accountId,omitempty"`
	Message   string `json:"message,omitempty"`
}

/*
2, not 1. The `vpn` block's states changed meaning with the switch-to-teardown rewrite, and a
`synchronized` field that no consumer could rely on was removed. Leaving the version at 1 while the
payload changes underneath is how a consumer silently stops matching and nobody finds out.
*/
const accountSwitchSchemaVersion = 2

type accountSwitchResult struct {
	SchemaVersion  int                `json:"schemaVersion"`
	AccountID      string             `json:"accountId"`
	AccountChanged bool               `json:"accountChanged"`
	VPN            vpnSynchronization `json:"vpn"`
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

	/*
	   TRY THE CREDENTIAL THIS MACHINE ALREADY HAS.

	   Switching used to sign in through the browser every single time, and
	   because the config held one refresh token it also signed you out of the
	   account you left. So moving between two accounts, which this product asks
	   people to do, cost a browser round-trip each way and lost the session
	   behind you. Reported by an operator moving between a provider account and
	   the tenant account it allocates to.

	   The stored credential is USED, not merely checked: a refresh token can be
	   revoked or expire and the only way to know is to spend it. So it becomes
	   active, gets exercised, and the browser is the fallback when it fails.
	   That keeps the old path intact for every case where there is nothing
	   stored, which includes the first switch after an upgrade.
	*/
	if switched, switchErr := switchWithStoredCredential(cmd, cfg, requestedAccountID, previousAccountID); switched {
		return switchErr
	}

	session, err := authenticateInBrowser(cfg, progress)
	if err != nil {
		return err
	}
	// The sign-in lands on the login's DEFAULT account, which is often not the one asked for. A
	// requested account the login is a member of is opened by the same session, so it becomes the
	// active account instead of failing the switch.
	memberships := sessionMemberships(cfg, session)
	activeAccountID, err := verifyRequestedAccount(requestedAccountID, session.AccountID, memberships)
	if err != nil {
		return err
	}
	session.AccountID = activeAccountID
	commitBrowserSession(cfg, session, memberships)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save account context: %w", err)
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	vpnResult := disconnectVPNForAccountChange(socketPath, previousAccountID, cfg.GetAccountID())
	result := accountSwitchResult{SchemaVersion: accountSwitchSchemaVersion, AccountID: session.AccountID, AccountChanged: previousAccountID != session.AccountID, VPN: vpnResult}
	return emitAccountResult(cmd, result, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Active account: %s\n", session.AccountID)
		if vpnResult.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), vpnResult.Message)
		}
	})
}

/*
Switch using the credential already on disk, or report that it could not.

Returns (false, nil) when there is nothing to try, which is the caller's signal
to open a browser. Returns (true, nil) on success and (true, err) only for a
failure the person has to act on: a failed save, or a stored credential whose
login is no longer a member of the account. Falling back to the browser on
every error would hide a real problem behind a login prompt.

The config is REVERTED when the credential is dead or its login is not a
member. Leaving a stale account active would mean the switch both failed and
changed the account, and the next command would talk to the wrong one.
*/
func switchWithStoredCredential(cmd *cobra.Command, cfg *config.Config, requestedAccountID, previousAccountID string) (bool, error) {
	if requestedAccountID == "" {
		return false, nil
	}

	/*
	   ALREADY ON IT IS NOT A REASON TO SIGN IN AGAIN.

	   This used to fall through to the browser, on the theory that people run
	   it to refresh a sign-in. Reported by an operator immediately: switching
	   to the account you are already on opened a browser, which reads as the
	   CLI having lost the session it had just used. Refreshing a sign-in is
	   what `runos login` is for.

	   The credential is still exercised, because "already on it" is unhelpful
	   when the token behind it is dead: the next command would be the one to
	   find out. A dead credential here falls through and signs in.
	*/
	if requestedAccountID == previousAccountID {
		if _, tokenErr := auth.ResolveToken(cfg); tokenErr != nil {
			return false, nil
		}
		socketPath, _ := cmd.Flags().GetString("socket")
		result := accountSwitchResult{
			SchemaVersion:  accountSwitchSchemaVersion,
			AccountID:      requestedAccountID,
			AccountChanged: false,
			VPN:            disconnectVPNForAccountChange(socketPath, previousAccountID, requestedAccountID),
		}
		return true, emitAccountResult(cmd, result, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Already on %s. Run 'runos login' to sign in again.\n", requestedAccountID)
		})
	}

	before := cfg.Clone()
	sharedActiveSession := shareActiveSessionWith(cfg, requestedAccountID)
	if !cfg.ActivateStoredAccount(requestedAccountID, time.Now().UTC().Format(time.RFC3339)) {
		*cfg = before
		return false, nil
	}
	token, tokenErr := auth.ResolveToken(cfg)
	if tokenErr != nil {
		*cfg = before
		fmt.Fprintf(cmd.ErrOrStderr(), "The saved sign-in for %s is no longer valid, so signing in again.\n", requestedAccountID)
		return false, nil
	}
	// shareActiveSessionWith only stores a session after conductor listed the account, so asking
	// again would spend a second round trip on an answer already in hand.
	if !sharedActiveSession {
		if err := confirmMembership(cfg, token, requestedAccountID, cmd.ErrOrStderr()); err != nil {
			*cfg = before
			return true, err
		}
	}
	if err := cfg.Save(); err != nil {
		*cfg = before
		return true, fmt.Errorf("failed to save account context: %w", err)
	}

	socketPath, _ := cmd.Flags().GetString("socket")
	vpnResult := disconnectVPNForAccountChange(socketPath, previousAccountID, cfg.GetAccountID())
	result := accountSwitchResult{
		SchemaVersion:  accountSwitchSchemaVersion,
		AccountID:      requestedAccountID,
		AccountChanged: previousAccountID != requestedAccountID,
		VPN:            vpnResult,
	}
	source := "used the saved sign-in"
	if sharedActiveSession {
		source = "used your current sign-in, which is a member"
	}
	return true, emitAccountResult(cmd, result, func() {
		fmt.Fprintf(cmd.OutOrStdout(), "Active account: %s (%s)\n", requestedAccountID, source)
		if vpnResult.Message != "" {
			fmt.Fprintln(cmd.OutOrStdout(), vpnResult.Message)
		}
	})
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

	resp, err := vpn.NewClient(socketPath).Call(vpn.Request{Op: vpn.OpDown})
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
	// A tunnel that was not up was not disconnected. The daemon is a boot-start root service, so
	// answering with no tunnel running is the ordinary state between sessions, and claiming a
	// disconnect there is the same class of unmeasured claim the update notices were fixed for.
	if !resp.TunnelWasUp {
		result.State = vpnStateUnchanged
		return result
	}
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
		/*
		 Forgetting the account you are ON is a sign-out, so it goes through the one helper that
		 knows what that means. Clearing the five credential fields by hand here missed
		 DefaultClusterID, which is account-scoped: forgetting the active account left its default
		 cluster behind, and the next command answered "Cluster <cid> not found in account <aid>",
		 which is the exact defect ClearSession was written to remove.
		*/
		cfg.ClearSession()
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
