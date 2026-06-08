package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

const (
	pollInterval = 2 * time.Second
	pollTimeout  = 5 * time.Minute
)

// accountIDCharset bounds a user-supplied account id before it is
// persisted and later joined into request paths (/:aid/...). Restricting
// to alphanumerics keeps a typo'd or hostile value from injecting path
// separators; the 1-64 bound is a generous superset of real ids.
var accountIDCharset = regexp.MustCompile(`^[a-zA-Z0-9]{1,64}$`)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with RunOS",
	Long: `Opens a browser to authenticate with RunOS using your existing account.

For headless / CI use, pass --api-key to store a personal access token
(PAT) instead of the browser flow:

  runos login --api-key <pat> --account-id <id>

The stored PAT is used for all subsequent commands and cleared by
'runos logout'. The RUNOS_API_KEY environment variable still takes
precedence over a stored key.`,
	RunE: runLogin,
}

func init() {
	loginCmd.Flags().String("api-key", "", "Authenticate with a personal access token (PAT) instead of the browser flow; pair with --account-id")
	loginCmd.Flags().String("account-id", "", "Account ID to store with --api-key (defaults to the account already in config, if any)")
}

// resolveLoginAccountID picks the account id to persist for an api-key
// login: the --account-id flag wins, otherwise any id already in config
// (from a prior login) is kept. A PAT addresses a specific account, so
// an empty result is an error rather than a silent no-account login.
func resolveLoginAccountID(flagAID, existingAID string) (string, error) {
	aid := strings.TrimSpace(flagAID)
	if aid == "" {
		aid = strings.TrimSpace(existingAID)
	}
	if aid == "" {
		return "", fmt.Errorf("--api-key requires an account id: pass --account-id <id> (find it in the console, or run a normal 'runos login' first)")
	}
	if !accountIDCharset.MatchString(aid) {
		return "", fmt.Errorf("account id %q is not a valid shape (expected alphanumeric, 1-64 chars)", aid)
	}
	return aid, nil
}

func runLogin(cmd *cobra.Command, args []string) error {
	if apiKey, _ := cmd.Flags().GetString("api-key"); strings.TrimSpace(apiKey) != "" {
		return loginWithAPIKey(cmd, apiKey)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initiate device auth with Conductor API
	conductorClient := api.NewClient(cfg.GetAPIURL())
	initResp, err := conductorClient.InitiateDeviceAuth()
	if err != nil {
		return fmt.Errorf("failed to initiate device auth: %w", err)
	}

	deviceID := initResp.DeviceID
	token := initResp.Token

	// Build browser URL with deviceId-token in path
	browserURL := fmt.Sprintf("%s/account/connect-device/%s-%s",
		cfg.GetConsoleURL(),
		deviceID,
		token,
	)

	fmt.Printf("Opening browser to authenticate...\n")
	fmt.Printf("Device ID: %s - verify this matches the browser\n", deviceID)

	if err := openBrowser(browserURL); err != nil {
		fmt.Printf("\nCouldn't open browser automatically (this is normal on remote servers).\n")
		fmt.Printf("Please open this URL in your browser:\n\n  %s\n\n", browserURL)
	} else {
		fmt.Printf("If the browser doesn't open, visit: %s\n\n", browserURL)
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

			cfg.ApplySessionLogin(
				resp.AccountID,
				&config.FirebaseConfig{
					APIKey:     resp.Firebase.APIKey,
					AuthDomain: resp.Firebase.AuthDomain,
					ProjectID:  resp.Firebase.ProjectID,
				},
				signIn.RefreshToken,
				time.Now().UTC().Format(time.RFC3339),
			)
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

// loginWithAPIKey persists a PAT to ~/.runos/config.json (0600) and
// switches the active credential to it, clearing any Firebase
// refresh-token state so exactly one credential is live and 'runos
// logout' has a single thing to clear. The token is never echoed back.
func loginWithAPIKey(cmd *cobra.Command, apiKey string) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	flagAID, _ := cmd.Flags().GetString("account-id")
	aid, err := resolveLoginAccountID(flagAID, cfg.AccountID)
	if err != nil {
		return err
	}

	cfg.APIKey = strings.TrimSpace(apiKey)
	cfg.AccountID = aid
	cfg.RefreshToken = ""
	cfg.Firebase = nil
	cfg.SignedInAt = time.Now().UTC().Format(time.RFC3339)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Printf("Stored API key for account %s.\n", aid)
	fmt.Println("The CLI will use this PAT for authentication. Run 'runos logout' to clear it.")
	fmt.Printf("Note: the %s environment variable, when set, still takes precedence over the stored key.\n", auth.APIKeyEnvVar)
	return nil
}
