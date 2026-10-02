package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
)

/*
`runos vpn diagnose`: one compact report for a person who cannot get the VPN working.

FCR349. The support loop was stuck because neither the user nor their assistant could see what had
happened. Sending a raw log failed (582 KB, one useful line), so this builds the report: the failed
step and what it was trying to do, the state of the daemon, the ids that identify the account, and a
short tail of the log. Everything in it is redacted, so it is safe to paste.

IT MUST WORK WHEN EVERYTHING ELSE DOES NOT. No daemon, no sign-in, no config, no network and no root
are the normal conditions of the person running it, so every input is optional and a missing one is
a line in the report rather than an error. It touches only local files and the daemon socket. It
makes no network call.
*/

var vpnDiagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "Print a redaction-safe report of the VPN state and the last failed step",
	Long: "Print one compact report: CLI version and platform, sign-in state, the VPN service and its\n" +
		"control socket, the tunnel status, the last failed connect step and what it was trying to do,\n" +
		"the last 'runos vpn up' attempt, and recent daemon log lines. No root needed. Secrets are never\n" +
		"included: no tokens, keys or WireGuard configuration. Account, device and cluster ids are.\n\n" +
		"Paste the output into a support request, or give it to your assistant. Use --json for an\n" +
		"assistant that reads structured output.",
	RunE: runVPNDiagnose,
}

func init() {
	vpnDiagnoseCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	vpnDiagnoseCmd.Flags().Int("lines", 40, "number of recent daemon log lines to include")
	vpnCmd.AddCommand(vpnDiagnoseCmd)
}

func runVPNDiagnose(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	lines, _ := cmd.Flags().GetInt("lines")
	report := vpn.BuildReport(collectReportInput(cmd, lines))
	if useJSON, _ := cmd.Flags().GetBool("json"); useJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}
	fmt.Fprint(cmd.OutOrStdout(), report.Text())
	return nil
}

// collectReportInput gathers every input the report wants. Each one that cannot be read is left
// empty and says so in the report; none of them fails the command.
func collectReportInput(cmd *cobra.Command, logLines int) vpn.ReportInput {
	in := vpn.ReportInput{
		Now: time.Now(), CLIVersion: version.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ServiceState: serviceState(),
	}
	// LoadLocal, not Load: with RUNOS_API_KEY set and no config file, Load fetches the default
	// environment from a CDN with a 10 second deadline, and this command promises no network call.
	cfg, err := config.LoadLocal()
	if errors.Is(err, config.ErrConfigNotFound) && os.Getenv(auth.APIKeyEnvVar) != "" {
		// A PAT caller needs no config file; the error would only send them to `runos login`.
		cfg, err = nil, nil
	}
	if err != nil {
		in.ConfigErr = err.Error()
	}
	if cfg != nil {
		in.AccountID = cfg.GetAccountID()
		in.APIURL = cfg.GetAPIURL()
	}
	kind := auth.Kind(cfg)
	in.SignedIn = kind != auth.CredentialNone
	in.CredentialKind = string(kind)

	in.Socket, in.Status = vpn.ProbeDaemon(vpnSocketClient(cmd))
	if dir, err := config.Dir(); err == nil {
		in.Trace = vpn.ReadTrace(filepath.Join(dir, upTraceFile))
	}
	in.DaemonLog = vpn.ReadLog(vpnLogPathsForRead(), logLines)
	return in
}

// serviceState asks the OS service manager. On Windows the service manager refuses a query from a
// non-elevated prompt, which would read as "not running" for a service that is; say unknown.
func serviceState() string {
	if runtime.GOOS == "windows" && !vpn.IsAdmin() {
		return "unknown (the Windows service manager needs an elevated prompt to say)"
	}
	running, err := vpn.NewService().Running()
	switch {
	case err != nil:
		return "unknown (" + err.Error() + ")"
	case running:
		return "running"
	default:
		return "not running"
	}
}
