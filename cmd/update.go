package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/runos-official/cli/internal/desktop"
	"github.com/runos-official/cli/internal/update"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{Use: "update", Short: "Update RunOS components", RunE: runUpdate}

func init() {
	updateCmd.Flags().BoolP("check", "c", false, "Only check for updates, don't install")
	updateCmd.Flags().BoolP("json", "j", false, "Output as JSON")
}

type updateComponentResult struct {
	Updated bool   `json:"updated"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
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
	result := combinedUpdateResult{SchemaVersion: 1, CLI: updateComponentResult{Version: currentVersion}}
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
			component := &updateComponentResult{Version: status.Version}
			result.Desktop = component
			if checkOnly {
				latest, latestErr := manager.LatestVersion()
				if latestErr != nil {
					return latestErr
				}
				component.Version = latest
				component.Message = "Desktop update check completed."
			} else {
				desktopResult, desktopErr := manager.Update()
				if desktopErr != nil {
					return desktopErr
				}
				component.Updated = desktopResult.Updated
				component.Version = desktopResult.Version
				component.Message = desktopResult.Message
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
	return nil
}
