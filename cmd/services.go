package cmd

import (
	"github.com/spf13/cobra"
)

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "Manage services",
	Long:  `Manage RunOS services. Use subcommands to list, show, add, or delete services.`,
}
