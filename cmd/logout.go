package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of RunOS",
	Long:  `Clear local authentication credentials. You will need to run 'runos login' to authenticate again.`,
	RunE:  runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	hasActiveAccount := false
	for _, account := range cfg.KnownAccounts {
		if account.Active {
			hasActiveAccount = true
			break
		}
	}

	// Report an existing logout only when no active account metadata remains.
	if cfg.RefreshToken == "" && cfg.Firebase == nil && cfg.AccountID == "" && cfg.APIKey == "" && !hasActiveAccount {
		fmt.Println("Already logged out.")
		return nil
	}

	// Clear authentication fields only (preserve URLs and default cluster)
	cfg.RefreshToken = ""
	cfg.Firebase = nil
	cfg.AccountID = ""
	cfg.SignedInAt = ""
	cfg.APIKey = ""
	cfg.ClearActiveAccount()

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}
