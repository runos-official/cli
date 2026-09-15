package cmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

var preauthCmd = &cobra.Command{
	Use:   "preauth",
	Short: "Authenticate with a pre-authorized device token",
	Long:  `Authenticates with RunOS using a pre-authorized device token, skipping the browser-based flow.`,
	RunE:  runPreauth,
}

func init() {
	preauthCmd.Flags().String("token", "", "Device auth token from Conductor")
	preauthCmd.Flags().String("device-id", "", "Device ID from Conductor")
	preauthCmd.MarkFlagRequired("token")
	preauthCmd.MarkFlagRequired("device-id")
	loginCmd.AddCommand(preauthCmd)
}

func runPreauth(cmd *cobra.Command, args []string) error {
	token, _ := cmd.Flags().GetString("token")
	deviceID, _ := cmd.Flags().GetString("device-id")

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	conductorClient := api.NewClient(cfg.GetAPIURL())
	resp, err := conductorClient.PollDeviceAuth(deviceID, token)
	if err != nil {
		return fmt.Errorf("failed to check authorization: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("authorization failed (error=%s): %s", resp.Error, resp.Message)
	}

	if resp.Firebase == nil {
		return fmt.Errorf("missing firebase config in response")
	}

	fmt.Printf("Exchanging token...")

	signIn, err := auth.ExchangeCustomToken(resp.CustomToken, resp.Firebase.APIKey)
	if err != nil {
		return fmt.Errorf("failed to exchange token: %w", err)
	}

	session := deviceAuthSession(resp, signIn)
	commitBrowserSession(cfg, session, sessionMemberships(cfg, session))
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Printf("\nAuthenticated successfully!\n")
	return nil
}
