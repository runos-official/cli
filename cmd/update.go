package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/update"

	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the CLI to the latest version",
	Long:  `Check for updates and download the latest CLI version from the RunOS CDN.`,
	RunE:  runUpdate,
}

func init() {
	updateCmd.Flags().BoolP("check", "c", false, "Only check for updates, don't install")
}

func runUpdate(cmd *cobra.Command, args []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check")

	updater, err := update.NewUpdater()
	if err != nil {
		return err
	}

	currentVersion := updater.CurrentVersion()
	fmt.Printf("Current version: %s\n", currentVersion)

	// Dev builds aren't semver-comparable. Mirrors the cli/version-check
	// endpoint's dev-build guard so the local `runos update` path doesn't
	// recommend installing a release on top of a fresh local build
	// (which would be a silent downgrade).
	if update.IsDevBuild() {
		fmt.Println("\nLocal version is a dev build (built via `make local`).")
		fmt.Println("Skipping update check; `runos update` would downgrade to the latest release.")
		fmt.Println("To switch to a release build, install via the published installer.")
		return nil
	}

	fmt.Println("Checking for updates...")
	latestVersion, err := updater.FetchLatestVersion()
	if err != nil {
		return err
	}

	fmt.Printf("Latest version:  %s\n", latestVersion)

	if !updater.NeedsUpdate(latestVersion) {
		fmt.Println("\nAlready up to date.")
		return nil
	}

	if checkOnly {
		fmt.Println("\nUpdate available. Run 'runos update' to install.")
		return nil
	}

	fmt.Println("\nDownloading update...")
	if err := updater.DownloadAndInstall(latestVersion); err != nil {
		return err
	}

	fmt.Printf("\nSuccessfully updated to %s\n", latestVersion)
	return nil
}
