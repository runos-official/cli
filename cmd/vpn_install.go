package cmd

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/runos-official/cli/internal/vpn"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
)

// install, uninstall and the hidden daemon. `install` writes the OS service that runs the daemon
// as root (needed once to create a network interface, decision 5) and says exactly what it wrote.
// `daemon` is the long-running root process itself, not run by hand.

var vpnInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the RunOS VPN service (needs admin once)",
	RunE:  runVPNInstall,
}

var vpnUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the RunOS VPN service",
	RunE:  runVPNUninstall,
}

var vpnRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the RunOS VPN service (needs admin), so it runs the current runos build",
	Long: "Restart the RunOS VPN service. The service runs the same runos binary this CLI updates in\n" +
		"place, so after an update the running daemon lags the binary on disk until it restarts;\n" +
		"the symptom is a version-skew error naming this command. Needs admin, because the service\n" +
		"runs as root. Keys, enrolment and the session survive a restart; the tunnel re-converges\n" +
		"on its own within seconds.",
	RunE: runVPNRestart,
}

var vpnDaemonCmd = &cobra.Command{
	Use:    "daemon",
	Short:  "Run the RunOS VPN daemon (managed by the service; not run by hand)",
	Hidden: true,
	RunE:   runVPNDaemon,
}

func init() {
	vpnDaemonCmd.Flags().String("socket-group", "", "group that may reach the control socket")
	// Written by `vpn install` only when a person named the group. Absent means the installer
	// derived it, which is what every machine installed by an older build looks like, so those
	// still self-heal.
	vpnDaemonCmd.Flags().String("socket-group-source", "", "'explicit' when a person named the socket group (set by 'vpn install')")
	// An override for a machine whose administrators are not in the group this would pick. The
	// message a person meets when the socket refuses them names this flag, so it has to exist.
	vpnInstallCmd.Flags().String("socket-group", "", "group that may reach the control socket (default: the installing user's group)")
	vpnDaemonCmd.Flags().String("state-dir", "", "override the daemon state directory (advanced/testing)")
	vpnDaemonCmd.Flags().Bool("verbose", false, "verbose WireGuard logging")
	// A hidden --socket override on the parent, for tests and non-default installs.
	vpnCmd.PersistentFlags().String("socket", "", "path to the daemon control socket (advanced)")
	_ = vpnCmd.PersistentFlags().MarkHidden("socket")
	vpnCmd.AddCommand(vpnInstallCmd, vpnUninstallCmd, vpnRestartCmd, vpnDaemonCmd)
}

func runVPNInstall(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if !vpn.IsAdmin() {
		return fmt.Errorf("installing the VPN service needs admin: %s", vpn.AdminHint)
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find the runos binary: %w", err)
	}
	/*
	 `Changed`, not emptiness. `--socket-group ""` is a deliberate request for a root-only socket,
	 and reading it as "not supplied" made that posture impossible to ask for while also letting
	 the daemon's self-heal overrule a group somebody had named.
	*/
	groupExplicit := cmd.Flags().Changed("socket-group")
	group, _ := cmd.Flags().GetString("socket-group")
	if !groupExplicit {
		group = socketGroupForInstall()
	}
	svc := vpn.NewService()
	if err := svc.Install(execPath, group, groupExplicit); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "RunOS VPN service installed.")
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", svc.Describe())
	fmt.Fprintln(cmd.OutOrStdout(), "  Next: runos vpn up   (signs you in and connects; no sudo needed from here on)")
	fmt.Fprintln(cmd.OutOrStdout(), "  Remove later with: sudo runos vpn uninstall")
	return nil
}

func runVPNRestart(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if !vpn.IsAdmin() {
		return fmt.Errorf("restarting the VPN service needs admin: %s", strings.Replace(vpn.AdminHint, "install", "restart", 1))
	}
	if err := vpn.NewService().Restart(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "RunOS VPN service restarted; it now runs the current runos build.")
	fmt.Fprintln(cmd.OutOrStdout(), "  The tunnel re-converges on its own. Check with: runos vpn status")
	return nil
}

func runVPNUninstall(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if !vpn.IsAdmin() {
		return fmt.Errorf("removing the VPN service needs admin: %s", strings.Replace(vpn.AdminHint, "install", "uninstall", 1))
	}
	if err := vpn.NewService().Uninstall(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "RunOS VPN service removed.")
	return nil
}

// runVPNDaemon is the root process: it builds the daemon, resumes any live session, serves the
// socket, and runs until the host (launchd, systemd, the Windows SCM, or a terminal) stops it. It
// skips the CLI's config/manifest bootstrap (see root.go), because as root its home is not the
// user's, and it never needs the manifest.
func runVPNDaemon(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	socketGroup, _ := cmd.Flags().GetString("socket-group")
	groupSource, _ := cmd.Flags().GetString("socket-group-source")
	verbose, _ := cmd.Flags().GetBool("verbose")
	socket, _ := cmd.Flags().GetString("socket")
	stateDir, _ := cmd.Flags().GetString("state-dir")
	if stateDir == "" {
		stateDir = vpn.StateDir
	}
	return vpn.RunDaemonHost(func() (func(), error) {
		d, err := vpn.NewDaemon(stateDir, version.Version, verbose)
		if err != nil {
			return nil, err
		}
		d.Resume()
		listener, err := vpn.Serve(d, orDefaultSocket(socket), socketGroup, groupSource == "explicit")
		if err != nil {
			d.Close()
			return nil, err
		}
		return func() {
			listener.Close()
			d.Close()
		}, nil
	})
}

func orDefaultSocket(path string) string {
	if path == "" {
		return vpn.SocketPath
	}
	return path
}

/*
socketGroupForInstall returns the group the control socket should belong to, so the installing
user's CLI reaches it without sudo.

REPORTED 2026-08-31 by two macOS users independently: after installing, every command answered "the
RunOS VPN service is not running. Run 'sudo runos vpn install' first." The service was running. Their
socket was `root:wheel`, they were in `admin`, and connect() returned EACCES. Reinstalling re-ran
this function and produced the same socket, so the advice was a loop.

THE FALLBACK WAS THE BUG. `wheel` is not written anywhere in this repository; it was COMPUTED. Under
`sudo` the real user is named by SUDO_USER and their primary group is the right answer. Without it,
under `sudo -i`, `sudo su -`, a root shell or a provisioning script, this used `user.Current()`,
which is root, whose primary group is `wheel` on macOS and `root` on Linux.

Neither contains any of the people who will run the CLI afterwards, so root's group is the ONE answer
guaranteed to be wrong: the whole point of the setting is that a person reaches the socket without
sudo. It now falls back to the machine's administrators group instead, and `--socket-group` overrides
both for anyone whose machine does not follow the convention.
*/
func socketGroupForInstall() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil && !vpn.IsRootGroup(u.Gid) {
			if g, err := user.LookupGroupId(u.Gid); err == nil {
				return g.Name
			}
		}
	}
	return vpn.AdminGroup()
}
