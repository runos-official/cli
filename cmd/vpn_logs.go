package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

/*
`runos vpn logs`: the command a support conversation starts with.

WHY IT EXISTS. The daemon's output has always gone to /var/log/runos-vpn.log, because the
LaunchDaemon sets StandardOutPath and StandardErrorPath. Asking somebody to send that file did not
work. Measured on a real machine 2026-09-01, while the VPN had been down for fourteen minutes: the
file held 582 KB, and all but one line of it was macOS `MallocStackLogging` chatter that the Go
runtime provokes and nobody can act on.

So this prints the file with that noise removed, newest last, capped to a readable amount. What
survives is what the daemon itself wrote.

READING IT DOES NOT NEED ROOT. The file is world-readable by design, so a person can produce their
own logs without sudo, which is the difference between a support request that includes them and one
that does not.
*/

// vpnLogPath matches StandardOutPath in the LaunchDaemon plist (internal/vpn/service_darwin.go).
const vpnLogPath = "/var/log/runos-vpn.log"

/*
Lines the daemon did not write and nobody can act on.

`MallocStackLogging` is emitted by the macOS allocator into the process's stderr whenever a tool
inherits the environment variable, and a Go binary spawning helpers produces two per invocation.
It is pure noise here: it says nothing about the tunnel and it is what buried the real content.
*/
func isDaemonLogNoise(line string) bool {
	return strings.Contains(line, "MallocStackLogging") || strings.TrimSpace(line) == ""
}

var vpnLogsTail int

var vpnLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Print the VPN daemon log, with the OS noise removed",
	Long: "Print what the RunOS VPN daemon has written to " + vpnLogPath + ".\n\n" +
		"Lines the daemon did not write (macOS MallocStackLogging chatter) are removed, because\n" +
		"they make up almost the whole file and say nothing about the tunnel.\n\n" +
		"Attach the output to a support request, or read it beside `runos vpn status --json`:\n" +
		"status says what the daemon believes NOW, and this says how it got there.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		file, err := os.Open(vpnLogPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf(
					"no VPN daemon log at %s. The daemon writes it, so this usually means the "+
						"service was never installed: run 'sudo runos vpn install'", vpnLogPath)
			}
			return fmt.Errorf("open %s: %w", vpnLogPath, err)
		}
		defer file.Close()

		var kept []string
		scanner := bufio.NewScanner(file)
		// A daemon line is short; this raises the cap only so one very long error cannot end the
		// scan early and silently truncate the log.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		skipped := 0
		for scanner.Scan() {
			line := scanner.Text()
			if isDaemonLogNoise(line) {
				skipped++
				continue
			}
			kept = append(kept, line)
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read %s: %w", vpnLogPath, err)
		}

		shown := kept
		if vpnLogsTail > 0 && len(kept) > vpnLogsTail {
			shown = kept[len(kept)-vpnLogsTail:]
		}
		for _, line := range shown {
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}

		// Say what was hidden and what was trimmed. A reader who cannot tell the difference between
		// "the daemon said nothing" and "this command dropped it" cannot trust either.
		if len(kept) == 0 {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nThe daemon has written nothing to %s (%d OS noise line(s) skipped).\n",
				vpnLogPath, skipped)
			return nil
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"\n%d daemon line(s)%s; %d OS noise line(s) skipped. Full file: %s\n",
			len(kept),
			map[bool]string{true: fmt.Sprintf(", showing the last %d", len(shown))}[len(shown) < len(kept)],
			skipped, vpnLogPath)
		return nil
	},
}

func init() {
	vpnLogsCmd.Flags().IntVar(&vpnLogsTail, "tail", 200,
		"show only the last N daemon lines (0 for all)")
	vpnCmd.AddCommand(vpnLogsCmd)
}
