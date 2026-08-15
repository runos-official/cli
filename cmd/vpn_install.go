package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"syscall"

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

var vpnDaemonCmd = &cobra.Command{
	Use:    "daemon",
	Short:  "Run the RunOS VPN daemon (managed by the service; not run by hand)",
	Hidden: true,
	RunE:   runVPNDaemon,
}

func init() {
	vpnDaemonCmd.Flags().String("socket-group", "", "group that may reach the control socket")
	vpnDaemonCmd.Flags().String("state-dir", "", "override the daemon state directory (advanced/testing)")
	vpnDaemonCmd.Flags().Bool("verbose", false, "verbose WireGuard logging")
	// A hidden --socket override on the parent, for tests and non-default installs.
	vpnCmd.PersistentFlags().String("socket", "", "path to the daemon control socket (advanced)")
	_ = vpnCmd.PersistentFlags().MarkHidden("socket")
	vpnCmd.AddCommand(vpnInstallCmd, vpnUninstallCmd, vpnDaemonCmd)
}

func runVPNInstall(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if os.Geteuid() != 0 {
		return fmt.Errorf("installing the VPN service needs admin: re-run with 'sudo runos vpn install'")
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find the runos binary: %w", err)
	}
	group := socketGroupForInstall()
	svc := vpn.NewService()
	if err := svc.Install(execPath, group); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "RunOS VPN service installed:")
	fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", svc.Describe(execPath))
	fmt.Fprintln(cmd.OutOrStdout(), "Next: run 'runos vpn up' to sign in and connect.")
	return nil
}

func runVPNUninstall(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	if os.Geteuid() != 0 {
		return fmt.Errorf("removing the VPN service needs admin: re-run with 'sudo runos vpn uninstall'")
	}
	if err := vpn.NewService().Uninstall(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "RunOS VPN service removed.")
	return nil
}

// runVPNDaemon is the root process: it builds the daemon, resumes any live session, serves the
// socket, and runs until signalled. It skips the CLI's config/manifest bootstrap (see root.go),
// because as root its home is /var/root, not the user's, and it never needs the manifest.
func runVPNDaemon(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	group, _ := cmd.Flags().GetString("socket-group")
	_ = group // reserved: the service already chowns the socket group at install
	verbose, _ := cmd.Flags().GetBool("verbose")

	stateDir, _ := cmd.Flags().GetString("state-dir")
	if stateDir == "" {
		stateDir = vpn.StateDir
	}
	d, err := vpn.NewDaemon(stateDir, version.Version, verbose)
	if err != nil {
		return err
	}
	d.Resume()

	socket, _ := cmd.Flags().GetString("socket")
	listener, err := vpn.Serve(d, orDefaultSocket(socket))
	if err != nil {
		return err
	}
	defer listener.Close()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	d.Close()
	return nil
}

func orDefaultSocket(path string) string {
	if path == "" {
		return vpn.SocketPath
	}
	return path
}

// socketGroupForInstall returns the group the control socket should belong to, so the installing
// user's CLI reaches it without sudo. Under `sudo`, SUDO_GID/SUDO_USER name the real user; fall
// back to the effective user's primary group.
func socketGroupForInstall() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			if g, err := user.LookupGroupId(u.Gid); err == nil {
				return g.Name
			}
		}
	}
	if u, err := user.Current(); err == nil {
		if g, err := user.LookupGroupId(u.Gid); err == nil {
			return g.Name
		}
	}
	return ""
}
