package cmd

import (
	"fmt"
	"net/url"
	"time"

	"cli/internal/api"
	"cli/internal/auth"
	"cli/internal/config"

	"github.com/spf13/cobra"
)

const (
	pollInterval = 2 * time.Second
	pollTimeout  = 5 * time.Minute
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with RunOS",
	Long:  `Opens a browser to authenticate with RunOS using your existing account.`,
	RunE:  runLogin,
}

func runLogin(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initiate device auth with Conductor API
	conductorClient := api.NewClient(cfg.GetConductorURL())
	initResp, err := conductorClient.InitiateDeviceAuth()
	if err != nil {
		return fmt.Errorf("failed to initiate device auth: %w", err)
	}

	deviceID := initResp.DeviceID
	token := initResp.Token

	// Build browser URL with query params
	browserURL := fmt.Sprintf("%s/auth/device?deviceId=%s&token=%s",
		cfg.GetConsoleURL(),
		url.QueryEscape(deviceID),
		url.QueryEscape(token),
	)

	fmt.Printf("Opening browser to authenticate...\n")
	fmt.Printf("Device ID: %s - verify this matches the browser\n", deviceID)
	fmt.Printf("If the browser doesn't open, visit: %s\n\n", browserURL)

	if err := openBrowser(browserURL); err != nil {
		return fmt.Errorf("failed to open browser: %w", err)
	}

	fmt.Printf("Waiting for authorization")

	deadline := time.Now().Add(pollTimeout)

	for time.Now().Before(deadline) {
		resp, err := conductorClient.PollDeviceAuth(deviceID, token)
		if err != nil {
			fmt.Printf("\n")
			return fmt.Errorf("failed to check authorization: %w", err)
		}

		if resp.Success {
			fmt.Printf("\n\nExchanging token...")

			if resp.Firebase == nil {
				return fmt.Errorf("missing firebase config in response")
			}

			signIn, err := auth.ExchangeCustomToken(resp.CustomToken, resp.Firebase.APIKey)
			if err != nil {
				return fmt.Errorf("failed to exchange token: %w", err)
			}

			cfg.AccountID = resp.AccountID
			cfg.Firebase = &config.FirebaseConfig{
				APIKey:     resp.Firebase.APIKey,
				AuthDomain: resp.Firebase.AuthDomain,
				ProjectID:  resp.Firebase.ProjectID,
			}
			cfg.RefreshToken = signIn.RefreshToken
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("failed to save credentials: %w", err)
			}

			fmt.Printf("\nAuthenticated successfully!\n")
			return nil
		}

		switch resp.Error {
		case "authorization_pending":
			fmt.Printf(".")
			time.Sleep(pollInterval)
			continue
		case "expired":
			fmt.Printf("\n")
			return fmt.Errorf("authorization expired - please try again")
		case "used":
			fmt.Printf("\n")
			return fmt.Errorf("token already used - please try again")
		case "invalid":
			fmt.Printf("\n")
			return fmt.Errorf("invalid request: %s", resp.Message)
		default:
			fmt.Printf("\n")
			return fmt.Errorf("authorization failed (error=%s): %s", resp.Error, resp.Message)
		}
	}

	fmt.Printf("\n")
	return fmt.Errorf("authorization timed out - please try again")
}
