package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// connect, disconnect and logout: the commands that change which clusters a live device reaches,
// and the one that forgets the device on this machine. connect/disconnect change the connected
// set through the daemon (which PUTs it to Conductor and re-polls); logout is destructive and
// gated by --yes.

var vpnConnectCmd = &cobra.Command{
	Use:   "connect <cid>",
	Short: "Connect the VPN to another cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runVPNConnect,
}

var vpnDisconnectCmd = &cobra.Command{
	Use:   "disconnect <cid>",
	Short: "Disconnect the VPN from a cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runVPNDisconnect,
}

var vpnLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Disconnect and forget this device's key on this machine",
	RunE:  runVPNLogout,
}

func init() {
	vpnConnectCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnDisconnectCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnLogoutCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnLogoutCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	vpnCmd.AddCommand(vpnConnectCmd, vpnDisconnectCmd, vpnLogoutCmd)
}

func runVPNConnect(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cid, err := normalizeClusterID(args[0])
	if err != nil {
		return err
	}
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpConnect, CID: cid})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func runVPNDisconnect(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cid, err := normalizeClusterID(args[0])
	if err != nil {
		return err
	}
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpDisconnect, CID: cid})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

func runVPNLogout(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if err := confirmVPNAction(cmd, "log out", "Disconnect and forget this device's key on this machine? Your account keeps the device until you revoke it, but this machine signs in fresh next time."); err != nil {
		return err
	}
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpLogout})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

// confirmVPNAction gates a destructive VPN command the same way the rest of the CLI does: --yes
// skips it, a non-TTY without --yes is a hard refuse (so a script cannot log out by accident), and
// a TTY prompts on stderr.
func confirmVPNAction(cmd *cobra.Command, verb, detail string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("%s is not reversible without a fresh sign-in; re-run with --yes to proceed", verb)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), detail)
	fmt.Fprint(cmd.ErrOrStderr(), "Proceed? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("could not read confirmation: %w", err)
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return fmt.Errorf("cancelled")
	}
	return nil
}
