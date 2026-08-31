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

/*
`forget-key`, and it used to be called `logout`.

Under FPL26 D1 that name became a trap. `runos logout` ends the IDENTITY and drops the tunnel with
it; this forgets the machine's KEY and leaves the identity alone. Two commands one word apart doing
unrelated things, one of them irreversible, is a defect in the vocabulary rather than the code.

`logout` stays as a hidden alias, because it is in scripts and in muscle memory. It is hidden rather
than listed, because the help is where somebody learns the words and it must teach the right ones.
*/
var vpnForgetKeyCmd = &cobra.Command{
	Use:     "forget-key",
	Aliases: []string{"logout"},
	Short:   "Disconnect and forget this machine's VPN key",
	Long: `Disconnect, and throw away the VPN key this machine holds.

The next 'runos vpn up' generates a new key and enrols it, which appears on your
account as a NEW device; the old one stays until you revoke it. Your sign-in is not
affected. To sign out, use 'runos logout'.`,
	RunE: runVPNForgetKey,
}

func init() {
	vpnConnectCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnDisconnectCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnForgetKeyCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnForgetKeyCmd.Flags().BoolP("yes", "y", false, "Skip the confirmation prompt")
	vpnCmd.AddCommand(vpnConnectCmd, vpnDisconnectCmd, vpnForgetKeyCmd)
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

/*
forgetKeyPrompt is what a person is asked before the key goes.

The old wording was "this machine signs in fresh next time", which is not what happens: nothing
signs in. A new key is generated and enrolled, and that shows up on the account as another device.
Measured on a live account 2026-08-31, which carries three rows for one laptop from earlier wipes.
The device row is the lasting effect, so it is the thing the prompt warns about.
*/
func forgetKeyPrompt() string {
	return "Disconnect and throw away this machine's VPN key?\n" +
		"The next 'runos vpn up' enrols a new key, which appears on your account as a NEW device; " +
		"the old one stays until you revoke it. Your sign-in is not affected."
}

func runVPNForgetKey(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if err := confirmVPNAction(cmd, "forget this machine's VPN key", forgetKeyPrompt()); err != nil {
		return err
	}
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpLogout})
	if err != nil {
		return err
	}
	return emitVPNStatus(cmd, resp.Status)
}

/*
What a non-TTY caller is told when it asked for something irreversible without saying so.

It used to read "<verb> is not reversible without a fresh sign-in", which was written for one
command and then inherited by another it was not true of: forgetting a key has nothing to do with
signing in. The sentence now says only what it can know, which is that the action does not undo.
*/
func confirmRefusal(verb string) error {
	return fmt.Errorf("%s cannot be undone; re-run with --yes to proceed", verb)
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
		return confirmRefusal(verb)
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
