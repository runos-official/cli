package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

// The `runos vpn` commands drive a local root daemon (internal/vpn) over a unix socket: this
// cannot be a manifest command because it talks to the daemon, not to an endpoint. The daemon
// holds the device key and the session token; the CLI holds the person's Firebase credential and
// does the sign-in, enrolment and session mint. `vpn devices list|show|rename|revoke` are the
// separate manifest commands that merge under this same parent.

var vpnCmd = &cobra.Command{
	Use:   "vpn",
	Short: "Connect this machine to your RunOS clusters over a VPN",
	Long: `Manage the RunOS VPN on this machine.

  runos vpn install      Install the VPN service (needs admin once)
  runos vpn up           Sign in and connect to your default cluster
  runos vpn status       Show the tunnel and each cluster
  runos vpn connect <cid> / disconnect <cid>
  runos vpn down         Disconnect and end the session
  runos vpn logout       Down, and forget this device's key on this machine

Each machine is a device with its own key and address. A sign-in lasts 24 hours;
after that the tunnel is cut and 'runos vpn up' signs you in again.`,
}

var vpnUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Sign in and bring the VPN up",
	RunE:  runVPNUp,
}

var vpnDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Disconnect and end the VPN session",
	RunE:  runVPNDown,
}

var vpnStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the VPN tunnel and each cluster",
	RunE:  runVPNStatus,
}

func init() {
	vpnUpCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnDownCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnStatusCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnCmd.AddCommand(vpnUpCmd, vpnDownCmd, vpnStatusCmd)
	rootCmd.AddCommand(vpnCmd)
}

// vpnSocketClient is the socket path the CLI dials. A hidden --socket flag on the parent overrides
// it for tests and for a non-default install; defaults to internal/vpn.SocketPath.
func vpnSocketClient(cmd *cobra.Command) *vpn.Client {
	path, _ := cmd.Flags().GetString("socket")
	return vpn.NewClient(path)
}

// refuseVPNWithPAT refuses to bring the VPN up under a stored secret. A tunnel is a person's
// session (decision 2: a sign-in is the 2FA gate), and a PAT is evidence of possession, never of a
// person being present. Reads/other commands are unaffected; only up/connect need a person.
func refuseVPNWithPAT(cfg *config.Config) error {
	if auth.Kind(cfg).IsPAT() {
		return fmt.Errorf("a personal access token cannot bring the VPN up: a tunnel is a person's 24-hour session, not a stored secret.\nSign in interactively with 'runos login' (or just run 'runos vpn up', which signs you in), then retry")
	}
	return nil
}

func runVPNUp(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := refuseVPNWithPAT(cfg); err != nil {
		return err
	}

	daemon := vpnSocketClient(cmd)

	// Ask the daemon for this machine's device key (it generates one on first use).
	identity, err := daemon.Call(vpn.Request{Op: vpn.OpIdentity})
	if err != nil {
		return err
	}
	publicKey := identity.Identity.PublicKey

	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return err
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "this-machine"
	}
	device, err := enrolDevice(cfg, token, publicKey, hostname, runtime.GOOS)
	if errors.Is(err, errKeyRevoked) {
		// The key was revoked (console, or an admin) and can never enrol again: rotate it in the
		// daemon and enrol the new one, so a revoked machine is one `up` from working again
		// rather than stuck forever. The old device row stays revoked in the account.
		fmt.Fprintln(cmd.ErrOrStderr(), "This machine's previous VPN key was revoked; enrolling a new one.")
		rotated, rErr := daemon.Call(vpn.Request{Op: vpn.OpRotateKey})
		if rErr != nil {
			return rErr
		}
		device, err = enrolDevice(cfg, token, rotated.Identity.PublicKey, hostname, runtime.GOOS)
	}
	if err != nil {
		return err
	}

	// Mint a session. Conductor requires a fresh interactive sign-in; on refusal, run the browser
	// flow once and retry with the new token. This is the whole re-auth story: no new command.
	session, needSignIn, err := mintSession(cfg, token, device.ID)
	if err != nil {
		return err
	}
	if needSignIn {
		fmt.Fprintln(cmd.ErrOrStderr(), "This VPN session needs a fresh sign-in.")
		if err := interactiveLogin(); err != nil {
			return err
		}
		if cfg, err = config.Load(); err != nil {
			return err
		}
		if token, err = auth.ResolveToken(cfg); err != nil {
			return err
		}
		if session, needSignIn, err = mintSession(cfg, token, device.ID); err != nil {
			return err
		}
		if needSignIn {
			return fmt.Errorf("the sign-in did not refresh the session window; try 'runos login' then 'runos vpn up' again")
		}
	}

	// Hand the session to the daemon, which brings the tunnel up and polls the desired state.
	up, err := daemon.Call(vpn.Request{
		Op:               vpn.OpUp,
		SessionToken:     session.Token,
		SessionExpiresAt: session.ExpiresAt,
		AccountID:        cfg.GetAccountID(),
		DeviceID:         device.ID,
		ConductorURL:     cfg.GetAPIURL(),
	})
	if err != nil {
		return err
	}

	// First connection: if the device is connected to nothing, connect it to the CLI's default
	// cluster (decision 3). A device that already has a connected set keeps it.
	status := up.Status
	if status != nil && !anyConnected(status) {
		if def := cfg.GetDefaultClusterID(); def != "" {
			if connected, cErr := daemon.Call(vpn.Request{Op: vpn.OpConnect, CID: def}); cErr == nil {
				status = connected.Status
			}
		}
	}
	return emitVPNStatus(cmd, status)
}

func runVPNDown(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpDown})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func runVPNStatus(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpStatus})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func anyConnected(status *vpn.Status) bool {
	for _, c := range status.Clusters {
		if c.Connected {
			return true
		}
	}
	return false
}

// emitVPNStatus renders a status both ways: the whole struct in --json mode, a human summary
// otherwise. The human view leads with the session (the thing that lapses) then each cluster.
func emitVPNStatus(cmd *cobra.Command, status *vpn.Status) error {
	jsonOut, _ := cmd.Flags().GetBool("json")
	if jsonOut {
		data, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}
	return printVPNStatus(cmd, status)
}
