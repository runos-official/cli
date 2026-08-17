package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/runos-official/cli/internal/desktop"
	"github.com/spf13/cobra"
)

var desktopCmd = &cobra.Command{Use: "desktop", Short: "Install and manage RunOS Desktop"}

var desktopInstallCmd = &cobra.Command{Use: "install", Short: "Install RunOS Desktop", RunE: runDesktopInstall}
var desktopUpdateCmd = &cobra.Command{Use: "update", Short: "Update RunOS Desktop", RunE: runDesktopUpdate}
var desktopUninstallCmd = &cobra.Command{Use: "uninstall", Short: "Uninstall RunOS Desktop", RunE: runDesktopUninstall}
var desktopStatusCmd = &cobra.Command{Use: "status", Short: "Show RunOS Desktop installation status", RunE: runDesktopStatus}
var desktopRelaunchCmd = &cobra.Command{Use: "relaunch", Short: "Relaunch RunOS Desktop after replacement", RunE: runDesktopRelaunch, Hidden: true}

func init() {
	desktopInstallCmd.Flags().String("version", "", "Install one Desktop version")
	for _, command := range []*cobra.Command{desktopInstallCmd, desktopUpdateCmd, desktopUninstallCmd, desktopStatusCmd} {
		command.Flags().BoolP("json", "j", false, "Output as JSON")
	}
	desktopRelaunchCmd.Flags().Int("wait-pid", 0, "Wait for this process before relaunching")
	_ = desktopRelaunchCmd.MarkFlagRequired("wait-pid")
	desktopCmd.AddCommand(desktopInstallCmd, desktopUpdateCmd, desktopUninstallCmd, desktopStatusCmd, desktopRelaunchCmd)
}

func runDesktopInstall(cmd *cobra.Command, _ []string) error {
	manager, err := desktop.NewManager()
	if err != nil {
		return err
	}
	version, _ := cmd.Flags().GetString("version")
	result, err := manager.Install(version)
	if err != nil {
		return err
	}
	return emitDesktopResult(cmd, result)
}

func runDesktopUpdate(cmd *cobra.Command, _ []string) error {
	manager, err := desktop.NewManager()
	if err != nil {
		return err
	}
	result, err := manager.Update()
	if err != nil {
		return err
	}
	return emitDesktopResult(cmd, result)
}

func runDesktopUninstall(cmd *cobra.Command, _ []string) error {
	manager, err := desktop.NewManager()
	if err != nil {
		return err
	}
	result, err := manager.Uninstall()
	if err != nil {
		return err
	}
	return emitDesktopResult(cmd, result)
}

func runDesktopStatus(cmd *cobra.Command, _ []string) error {
	manager, err := desktop.NewManager()
	if err != nil {
		return err
	}
	result, err := manager.Status()
	if err != nil {
		return err
	}
	return emitDesktopResult(cmd, result)
}

func runDesktopRelaunch(cmd *cobra.Command, _ []string) error {
	manager, err := desktop.NewManager()
	if err != nil {
		return err
	}
	pid, _ := cmd.Flags().GetInt("wait-pid")
	return manager.Relaunch(pid)
}

func emitDesktopResult(cmd *cobra.Command, result *desktop.Result) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}
	if result.Message != "" {
		fmt.Fprintln(cmd.OutOrStdout(), result.Message)
	}
	if result.Installed {
		fmt.Fprintf(cmd.OutOrStdout(), "RunOS Desktop %s is installed at %s.\n", result.Version, result.Path)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "RunOS Desktop is not installed.")
	}
	return nil
}
