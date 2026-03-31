package cmd

import (
	"fmt"

	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the CLI version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Version)
	},
}
