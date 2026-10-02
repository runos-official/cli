package cmd

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

/*
`runos vpn logs`: the command a support conversation starts with.

WHY IT EXISTS. The daemon's output used to go only to /var/log/runos-vpn.log, because the LaunchDaemon
sets StandardOutPath and StandardErrorPath. Asking somebody to send that file did not work. Measured
on a real machine 2026-09-01, while the VPN had been down for fourteen minutes: the file held 582 KB,
and all but one line of it was macOS `MallocStackLogging` chatter that the Go runtime provokes and
nobody can act on.

So this prints the log with that noise removed, newest last, capped to a readable amount. What
survives is what the daemon itself wrote.

EVERY PLATFORM, NOW. Only the macOS service definition ever named a log file; the systemd unit and the
Windows service named none, so on those two this command read a file that did not exist. The daemon
now writes its own bounded log (internal/vpn/logfile.go) on all three, and this reads it.

READING IT DOES NOT NEED ROOT, for the people the VPN is installed for. On a machine with a control
socket group the file is readable by that group (0640); with none configured it is world readable.
Somebody outside the group gets a plain "cannot read it" with the way round it, never "no log".
*/

// vpnLogPathsForRead is a variable so a test can point it at a temp file.
var vpnLogPathsForRead = vpn.DefaultLogPaths

var vpnLogsTail int

var vpnLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Print the VPN daemon log, with the OS noise removed",
	Long: "Print what the RunOS VPN daemon has written to its log, newest last.\n\n" +
		"Each connect step is one line: step=<name> status=ok|failed|warn, then the detail. Lines the\n" +
		"daemon did not write (macOS MallocStackLogging chatter) are removed, and anything that looks\n" +
		"like a credential is redacted.\n\n" +
		"For one compact report to paste into a support request, or to hand to an assistant, run\n" +
		"'runos vpn diagnose'.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		res := vpn.ReadLog(vpnLogPathsForRead(), vpnLogsTail)
		if res.Err != nil && len(res.Lines) == 0 {
			return errors.New(res.Problem())
		}
		if res.Path == "" {
			return fmt.Errorf("no VPN daemon log found. %s", vpn.MissingLogNote(runtime.GOOS))
		}
		for _, line := range res.Lines {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		if res.Err != nil {
			// Part of the log was read; say what was not, so a partial log is not mistaken for all of it.
			fmt.Fprintf(cmd.ErrOrStderr(), "\nwarning: %s\n", res.Problem())
		}

		// Say what was hidden and what was trimmed. A reader who cannot tell the difference between
		// "the daemon said nothing" and "this command dropped it" cannot trust either.
		if res.Total == 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nThe daemon has written nothing to %s (%d OS noise line(s) skipped).\n",
				res.Path, res.Skipped)
			return nil
		}
		trimmed := ""
		if len(res.Lines) < res.Total {
			trimmed = fmt.Sprintf(", showing the last %d", len(res.Lines))
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"\n%d daemon line(s)%s; %d OS noise line(s) skipped. Full file: %s\n",
			res.Total, trimmed, res.Skipped, res.Path)
		return nil
	},
}

func init() {
	vpnLogsCmd.Flags().IntVar(&vpnLogsTail, "tail", 200,
		"show only the last N daemon lines (0 for as many as are kept, up to 10000)")
	vpnCmd.AddCommand(vpnLogsCmd)
}
