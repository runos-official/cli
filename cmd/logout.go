package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of RunOS",
	Long:  `Clear local authentication credentials. You will need to run 'runos login' to authenticate again.`,
	RunE:  runLogout,
}

func init() {
	// The same hidden escape hatch every other daemon-talking command has. `logout` now drops the
	// tunnel (D3), so it needs the same way to name a non-default socket.
	logoutCmd.Flags().String("socket", "", "path to the daemon control socket (advanced)")
	_ = logoutCmd.Flags().MarkHidden("socket")
}

func runLogout(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	hasActiveAccount := false
	for _, account := range cfg.KnownAccounts {
		if account.Active {
			hasActiveAccount = true
			break
		}
	}

	// Report an existing logout only when no active account metadata remains.
	if cfg.RefreshToken == "" && cfg.Firebase == nil && cfg.AccountID == "" && cfg.APIKey == "" && !hasActiveAccount {
		fmt.Println("Already logged out.")
		return nil
	}

	/*
	 THE TUNNEL NEVER OUTLIVES THE IDENTITY THAT OPENED IT (FPL26 D3).

	 Logging out used to clear the credentials and say nothing to the VPN daemon, which holds its own
	 per-account session in a root process this file cannot reach by clearing a JSON file. The result
	 was one machine reporting `"authenticated": false` and `"vpnRunning": true` from the same
	 command: signed out, and still carrying traffic on the account it was signed out of.

	 BEST EFFORT, and it must stay that way. A machine with no VPN service installed, or a daemon
	 that is not running, must still be able to log out; the socket failing is the ordinary case on
	 those machines, not an error worth stopping for.

	 `OpDown`, NOT `OpLogout`. The daemon's logout op ALSO forgets this machine's key, and enrolment
	 is idempotent on the public key, so a forgotten key means the next `vpn up` enrols a brand new
	 device and leaves the old row behind for ever. Measured on a live account 2026-08-31: it already
	 carries three rows for one laptop, from earlier key wipes. Signing out must not add a fourth.
	 The key is harmless on its own; it opens nothing without a session token, and this has just
	 ended the session. Forgetting a key is what `runos vpn forget-key` is for.
	*/
	if resp, vErr := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpDown}); vErr == nil {
		if resp.Status != nil {
			fmt.Println("Disconnected the VPN.")
		}
	}

	// The environment survives; everything about the person does not, the default cluster included.
	// See Config.ClearSession.
	cfg.ClearSession()

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}
