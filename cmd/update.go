package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/runos-official/cli/internal/desktop"
	"github.com/runos-official/cli/internal/update"
	"github.com/runos-official/cli/internal/vpn"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{Use: "update", Short: "Update RunOS components", RunE: runUpdate}

func init() {
	updateCmd.Flags().BoolP("check", "c", false, "Only check for updates, don't install")
	updateCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	// This command dials the daemon to read its build, so it needs the same hidden escape hatch
	// every other daemon-dialling command has. Without the declaration `vpnSocketClient` silently
	// falls back to the production socket, which is invisible because the lookup error is dropped.
	registerSocketFlag(updateCmd)
}

/*
One component's update state.

`updated` and `version` keep their old meanings so an older reader is unaffected: `updated` is
whether this run INSTALLED anything, and `version` is the latest known version. Neither answers the
question a UI actually asks, which is "is there something to install?"

MEASURED 2026-08-31: with `--check`, `updated` is false whether you are current or six versions
behind, and `version` is the latest in both cases, so there was nothing to compare it against. The
only difference was an English sentence, and for the desktop the two payloads were byte-identical.
RunOS Desktop needs the verdict to disable its Update item and to badge its menu bar.

`updateAvailable` is that verdict and `currentVersion` is what is actually installed. Both are
additive.
*/
type updateComponentResult struct {
	Updated bool `json:"updated"`
	// Whether something is available to install. A FLAG, not a sentence to match on.
	UpdateAvailable bool `json:"updateAvailable"`
	// What is installed right now. `Version` stays the LATEST known version.
	CurrentVersion string `json:"currentVersion,omitempty"`
	Version        string `json:"version,omitempty"`
	Message        string `json:"message,omitempty"`
}

/*
Whether a desktop update is available, by the same rule the installer uses.

`Manager.Update()` treats `installed == latest` as up to date and installs otherwise, so the check
has to agree with it. A check that disagreed would either offer an update that does nothing, or hide
one that would have worked.

An empty side is never an update: nothing installed is the install path's business, and a latest
that could not be read is not evidence of anything.
*/
func desktopUpdateAvailable(installed, latest string) bool {
	if installed == "" || latest == "" {
		return false
	}
	// NEWER, not merely different. Any-difference downgraded a build that was ahead of the latest
	// release, which was invisible while every local build claimed 0.1.0 and became reachable the
	// moment local builds started reporting the version under development.
	return update.IsNewerVersion(latest, installed)
}

type combinedUpdateResult struct {
	SchemaVersion int                    `json:"schemaVersion"`
	CLI           updateComponentResult  `json:"cli"`
	Desktop       *updateComponentResult `json:"desktop,omitempty"`
}

func runUpdate(cmd *cobra.Command, _ []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check")
	jsonOutput, _ := cmd.Flags().GetBool("json")
	progress := cmd.OutOrStdout()
	if jsonOutput {
		progress = cmd.ErrOrStderr()
	}

	updater, err := update.NewUpdater()
	if err != nil {
		return err
	}
	updater.SetProgress(progress)
	currentVersion := updater.CurrentVersion()
	result := combinedUpdateResult{SchemaVersion: 1, CLI: updateComponentResult{
		Version:        currentVersion,
		CurrentVersion: currentVersion,
	}}
	fmt.Fprintf(progress, "Current CLI version: %s\n", currentVersion)

	if update.IsDevBuild() {
		result.CLI.Message = "Local CLI development build. The CLI update was skipped."
		fmt.Fprintln(progress, result.CLI.Message)
	} else {
		latestVersion, fetchErr := updater.FetchLatestVersion()
		if fetchErr != nil {
			return fetchErr
		}
		result.CLI.Version = latestVersion
		if !updater.NeedsUpdate(latestVersion) {
			result.CLI.Message = "The CLI is already up to date."
		} else if checkOnly {
			result.CLI.UpdateAvailable = true
			result.CLI.Message = "A CLI update is available."
		} else {
			fmt.Fprintln(progress, "Downloading the CLI update…")
			if err := updater.DownloadAndInstall(latestVersion); err != nil {
				return err
			}
			result.CLI.Updated = true
			result.CLI.Message = "The CLI update completed."
		}
		fmt.Fprintln(progress, result.CLI.Message)
	}

	if runtime.GOOS == "darwin" {
		manager, managerErr := desktop.NewManager()
		if managerErr != nil {
			return managerErr
		}
		status, statusErr := manager.Status()
		if statusErr != nil {
			return statusErr
		}
		if status.Installed {
			component := &updateComponentResult{Version: status.Version, CurrentVersion: status.Version}
			result.Desktop = component
			if checkOnly {
				latest, latestErr := manager.LatestVersion()
				if latestErr != nil {
					return latestErr
				}
				component.Version = latest
				component.UpdateAvailable = desktopUpdateAvailable(status.Version, latest)
				if component.UpdateAvailable {
					component.Message = "A RunOS Desktop update is available."
				} else {
					component.Message = "RunOS Desktop is already up to date."
				}
			} else {
				desktopResult, desktopErr := manager.Update()
				if desktopErr != nil {
					return desktopErr
				}
				component.Updated = desktopResult.Updated
				component.Version = desktopResult.Version
				component.Message = desktopResult.Message
				// It has just been installed, so nothing is waiting any more.
				component.CurrentVersion = desktopResult.Version
			}
			fmt.Fprintln(progress, component.Message)
		}
	}

	if jsonOutput {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	}

	// THE VPN DAEMON RUNS THIS BINARY, and replacing the file does not change what is already
	// loaded. The launchd plist points at the runos executable, so after an update the daemon
	// carries on running the PREVIOUS build until something restarts it, and nothing said so:
	// `runos vpn restart` exists and its own success message is "it now runs the current runos
	// build", but no code path called it and no code path mentioned it.
	//
	// SAID, NOT DONE. Restarting needs admin and `runos update` does not, so escalating here would
	// either fail or prompt for a password in the middle of an unrelated command. Naming the one
	// command is the honest half, and it is stated only when a daemon is actually loaded, so a
	// machine that never installed the VPN reads nothing about it.
	if running, err := vpn.NewService().Running(); err == nil && running {
		daemonVersion := runningDaemonVersion(cmd)
		installed := installedCLIVersion(currentVersion, result.CLI)
		if vpnDaemonNeedsRestart(true, daemonVersion, installed) {
			fmt.Fprint(progress, vpnRestartNotice(daemonVersion, installed))
		}
	}
	return nil
}

/*
Which build is now ON DISK, which is the one a restarted daemon would load.

NOT `version.Version`. That is the constant compiled into the RUNNING process, and installing an
update replaces the file and never re-execs, so it still holds the OLD version for the rest of the
command. Comparing the daemon against it meant that on the ordinary machine, where daemon and CLI
were in step, `runos update` installed a new build, the loaded daemon was genuinely behind it, and
the comparison was the old version against itself. The notice said nothing, on every real update,
which is the one case it exists for.

The update result already carries what it installed, so this asks it rather than the process.
*/
func installedCLIVersion(runningVersion string, cli updateComponentResult) string {
	if cli.Updated && cli.Version != "" {
		return cli.Version
	}
	return runningVersion
}

/*
Whether the loaded daemon is actually behind this binary.

REPORTED 2026-08-31: this notice printed on an already-current machine, where nothing had been
replaced and nothing could be running a previous build. The condition was only "is the VPN service
running", which is true on every machine that has one, so the advice appeared on every update
whether or not one happened, and sent people to type a sudo command that would do nothing.

A skew is `daemon != cli` and nothing else, which is the same comparison `runos vpn status` already
makes. A daemon too old to report its own version cannot be compared, and advice given on no
evidence is noise, so that stays quiet too.
*/
func vpnDaemonNeedsRestart(running bool, daemonVersion, cliVersion string) bool {
	return running && daemonVersion != "" && daemonVersion != cliVersion
}

// vpnRestartNotice names both builds, so the reader can see the skew is real rather than take it on
// trust, and the one command that fixes it. `runos update` cannot do it itself: restarting needs
// admin and update does not, so escalating here would prompt for a password mid-command.
func vpnRestartNotice(daemonVersion, cliVersion string) string {
	return fmt.Sprintf(
		"\nThe VPN service is running a different build (%s) from the one now installed (%s):\n"+
			"replacing the binary does not reload a daemon that is already loaded.\n"+
			"  Pick it up with: sudo runos vpn restart\n",
		daemonVersion, cliVersion,
	)
}

// runningDaemonVersion asks the daemon what build it is. Empty when it cannot be reached or is too
// old to say, which the caller treats as "no evidence of a skew".
func runningDaemonVersion(cmd *cobra.Command) string {
	resp, err := vpnSocketClient(cmd).Call(vpn.Request{Op: vpn.OpStatus})
	if err != nil || resp.Status == nil {
		return ""
	}
	return resp.Status.Version
}
