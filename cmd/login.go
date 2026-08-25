package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"

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

--account-id is required with --api-key: a PAT is scoped to one account,
so it must be paired with the matching account id explicitly (no fallback
to any account already in config).

The stored PAT is used for all subsequent commands and cleared by
'runos logout'. The RUNOS_API_KEY environment variable still takes
precedence over a stored key.`,
	RunE: runLogin,
}

func init() {
	loginCmd.Flags().BoolP("json", "j", false, "Report the browser sign-in as a stream of JSON events, one per line (device_code, pending, authorized, error)")
	loginCmd.Flags().String("api-key", "", "Authenticate with a personal access token (PAT) instead of the browser flow; pair with --account-id")
	loginCmd.Flags().String("account-id", "", "Account ID to store with --api-key (required with --api-key; the PAT is account-scoped)")
}

// resolveLoginAccountID validates the account id to persist for an
// api-key login. --account-id is mandatory: a PAT is account-scoped, so
// falling back to a stale config value would silently store the new PAT
// against the wrong tenant (auth succeeds by token, every /:aid/...
// request targets the wrong account). An empty flag is therefore a hard
// error, never a fallback. The CI env path (RUNOS_API_KEY +
// RUNOS_ACCOUNT_ID) pairs them explicitly and does not use this.
func resolveLoginAccountID(flagAID string) (string, error) {
	aid := strings.TrimSpace(flagAID)
	if aid == "" {
		return "", fmt.Errorf("--api-key requires --account-id <id> (the PAT is account-scoped; find the id in the console)")
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
	if useJSON, _ := cmd.Flags().GetBool("json"); useJSON {
		// The device id and the URL become readable by something other than a person: RunOS Desktop
		// shows them in a window so the id can be checked against the browser and the URL used when
		// the browser does not open.
		return interactiveLoginReporting(&jsonSignIn{out: cmd.OutOrStdout()}, false)
	}
	return interactiveLogin()
}

// interactiveLogin runs the browser device-code flow and saves the resulting Firebase session.
// Extracted from runLogin so `runos vpn up` can force a FRESH interactive sign-in (a VPN session
// needs an auth_time from the last few minutes, which a refreshed token does not carry). Returns
// after "Authenticated successfully!" is printed, or an error.
func interactiveLogin() error {
	return interactiveLoginReporting(textSignIn{out: os.Stdout}, true)
}

/*
The same sign-in, reporting through `report`.

`chatty` is separate from the reporter on purpose. The closing "Authenticated successfully!" and
the manifest-cache notice are prose for a terminal; written into a JSON stream they would corrupt
the last line a caller is parsing, and a caller reading events already knows it succeeded because
it was told.
*/
func interactiveLoginReporting(report signInReporter, chatty bool) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	session, err := browserAuthenticateReporting(cfg, report)
	if err != nil {
		return err
	}
	commitBrowserSession(cfg, session)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}
	if chatty {
		fmt.Printf("\nAuthenticated successfully!\n")
		warmManifestCache(cfg)
	}
	return nil
}

type browserSession struct {
	AccountID    string
	Firebase     *config.FirebaseConfig
	RefreshToken string
	SignedInAt   string
}

var authenticateInBrowser = browserAuthenticate

func commitBrowserSession(cfg *config.Config, session browserSession) {
	cfg.ApplySessionLogin(session.AccountID, session.Firebase, session.RefreshToken, session.SignedInAt)
}

// browserAuthenticate completes browser authentication without changing local context.
func browserAuthenticate(cfg *config.Config, progress io.Writer) (browserSession, error) {
	return browserAuthenticateReporting(cfg, textSignIn{out: progress})
}

/*
The device-code flow itself, reporting through `report` rather than writing prose.

One flow, two reporters (see login_events.go). The state transitions live HERE and are not
duplicated for the JSON form: a second code path for authentication is exactly the thing that
drifts and then differs in the one case nobody tested.
*/
func browserAuthenticateReporting(cfg *config.Config, report signInReporter) (browserSession, error) {

	// Initiate device auth with Conductor API
	conductorClient := api.NewClient(cfg.GetAPIURL())
	initResp, err := conductorClient.InitiateDeviceAuth()
	if err != nil {
		return browserSession{}, fmt.Errorf("failed to initiate device auth: %w", err)
	}

	deviceID := initResp.DeviceID
	token := initResp.Token

	// Build browser URL with deviceId-token in path
	browserURL := fmt.Sprintf("%s/account/connect-device/%s-%s",
		cfg.GetConsoleURL(),
		deviceID,
		token,
	)

	report.DeviceCode(deviceID, browserURL, openBrowser(browserURL) == nil)

	deadline := time.Now().Add(pollTimeout)

	for time.Now().Before(deadline) {
		resp, err := conductorClient.PollDeviceAuth(deviceID, token)
		if err != nil {
			report.Failed("unreachable", err.Error())
			return browserSession{}, fmt.Errorf("failed to check authorization: %w", err)
		}

		if resp.Success {
			report.Authorized()

			if resp.Firebase == nil {
				return browserSession{}, fmt.Errorf("missing firebase config in response")
			}

			signIn, err := auth.ExchangeCustomToken(resp.CustomToken, resp.Firebase.APIKey)
			if err != nil {
				return browserSession{}, fmt.Errorf("failed to exchange token: %w", err)
			}
			return browserSession{
				AccountID: resp.AccountID,
				Firebase: &config.FirebaseConfig{
					APIKey:     resp.Firebase.APIKey,
					AuthDomain: resp.Firebase.AuthDomain,
					ProjectID:  resp.Firebase.ProjectID,
				},
				RefreshToken: signIn.RefreshToken,
				SignedInAt:   time.Now().UTC().Format(time.RFC3339),
			}, nil
		}

		switch resp.Error {
		case "authorization_pending":
			report.Pending()
			time.Sleep(pollInterval)
			continue
		case "expired":
			report.Failed("expired", "authorization expired - please try again")
			return browserSession{}, fmt.Errorf("authorization expired - please try again")
		case "used":
			report.Failed("used", "token already used - please try again")
			return browserSession{}, fmt.Errorf("token already used - please try again")
		case "invalid":
			report.Failed("invalid", resp.Message)
			return browserSession{}, fmt.Errorf("invalid request: %s", resp.Message)
		default:
			report.Failed(resp.Error, resp.Message)
			return browserSession{}, fmt.Errorf("authorization failed (error=%s): %s", resp.Error, resp.Message)
		}
	}

	report.Failed("timeout", "authorization timed out - please try again")
	return browserSession{}, fmt.Errorf("authorization timed out - please try again")
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
	aid, err := resolveLoginAccountID(flagAID)
	if err != nil {
		return err
	}

	cfg.APIKey = strings.TrimSpace(apiKey)
	cfg.AccountID = aid
	cfg.RefreshToken = ""
	cfg.Firebase = nil
	cfg.SignedInAt = time.Now().UTC().Format(time.RFC3339)
	cfg.RememberAccount(aid, cfg.SignedInAt)
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Printf("Stored API key for account %s.\n", aid)
	fmt.Println("The CLI will use this PAT for authentication. Run 'runos logout' to clear it.")
	fmt.Printf("Note: the %s environment variable, when set, still takes precedence over the stored key.\n", auth.APIKeyEnvVar)
	warmManifestCache(cfg)
	return nil
}

// warmManifestCache best-effort fetches the CLI manifest right after a
// successful login so the dynamic commands (apps show, services list, ...)
// are available on the very next invocation, with no manual 'runos
// manifest update' step. Silent on failure: the next command's first-run
// bootstrap retries the fetch.
func warmManifestCache(cfg *config.Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	configDir := filepath.Join(home, ".runos")
	loader := manifest.NewLoader(cfg.GetAPIURL(), configDir)
	if _, err := loader.Load(); err != nil {
		return
	}
	fmt.Println("Run 'runos --help' to see available commands.")
}
