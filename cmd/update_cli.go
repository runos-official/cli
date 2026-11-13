package cmd

import (
	"fmt"
	"io"

	"github.com/runos-official/cli/internal/update"
	"github.com/spf13/cobra"
)

// devBuildSkipHint is the one line a development build without --force gets on stderr.
const devBuildSkipHint = "Use runos update --force to replace it with the published release."

/*
updateCLIComponent is the CLI half of `runos update`: it fills result.CLI and prints progress.

Without --force it is exactly the old behaviour: a development build skips (and now says how to
force), and a released build installs a NEWER release only. With --force it installs the latest
FINAL release over whatever is running, a development build or a build newer than the release
included, and says what is replaced and by what BEFORE it downloads. The download is the same
DownloadAndInstall call either way, so the checksum check and every other verification of the
normal update apply unchanged. --force is explicit only: nothing else reaches the forced path.
*/
func updateCLIComponent(cmd *cobra.Command, updater *update.Updater, checkOnly, force bool, progress io.Writer, result *combinedUpdateResult) error {
	if force {
		return forceCLIUpdate(cmd, updater, checkOnly, progress, result)
	}
	if update.IsDevBuild() {
		result.CLI.Message = "Local CLI development build. The CLI update was skipped."
		fmt.Fprintln(progress, result.CLI.Message)
		fmt.Fprintln(cmd.ErrOrStderr(), devBuildSkipHint)
		return nil
	}
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
		if status := daemonStatus(cmd); status != nil {
			fmt.Fprint(progress, vpnTunnelUpdateNotice(status.Running))
		}
		fmt.Fprintln(progress, "Downloading the CLI update…")
		if err := updater.DownloadAndInstall(latestVersion); err != nil {
			return err
		}
		result.CLI.Updated = true
		result.CLI.Message = "The CLI update completed."
	}
	fmt.Fprintln(progress, result.CLI.Message)
	return nil
}

// forceCLIUpdate is the --force path of updateCLIComponent.
func forceCLIUpdate(cmd *cobra.Command, updater *update.Updater, checkOnly bool, progress io.Writer, result *combinedUpdateResult) error {
	latest, err := updater.FetchLatestVersion()
	if err != nil {
		return err
	}
	// Only a final release, whatever the latest version says: checked before anything is shown or fetched.
	latest, err = update.FinalRelease(latest)
	if err != nil {
		return err
	}
	running := updater.CurrentVersion()
	plan := update.PlanForce(running, latest)
	result.CLI.Force = true
	result.CLI.Version = latest
	result.CLI.PreviousVersion = running
	result.CLI.NewVersion = latest
	fmt.Fprintln(progress, plan.Describe())

	if checkOnly {
		result.CLI.UpdateAvailable = plan.Kind != update.ForceReinstall
		result.CLI.Message = "With --force this would be replaced by the published release. Nothing was installed."
		fmt.Fprintln(progress, result.CLI.Message)
		return nil
	}
	if status := daemonStatus(cmd); status != nil {
		fmt.Fprint(progress, vpnTunnelUpdateNotice(status.Running))
	}
	fmt.Fprintln(progress, "Downloading the CLI update…")
	if err := updater.DownloadAndInstall(latest); err != nil {
		return err
	}
	result.CLI.Updated = true
	result.CLI.Message = fmt.Sprintf("The CLI was replaced with the published release v%s.", latest)
	fmt.Fprintln(progress, result.CLI.Message)
	return nil
}
