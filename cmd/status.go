package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

// authMethod names the credential the CLI would use for outgoing
// requests, in auth.ResolveToken's priority order. authNone means no
// credential is present.
type authMethod string

const (
	authNone      authMethod = "none"
	authPATEnv    authMethod = "pat-env"    // RUNOS_API_KEY
	authPATStored authMethod = "pat-stored" // cfg.APIKey, set by `runos login --api-key`
	authFirebase  authMethod = "firebase"   // interactive refresh-token session
)

// resolveAuthMethod reports which credential the CLI would use, mirroring
// auth.ResolveToken's priority (RUNOS_API_KEY -> stored PAT -> Firebase)
// without any network round-trip, so `runos status` recognises a PAT as
// authenticated instead of only honouring the Firebase fields. apiKeyEnv
// is passed in (the RUNOS_API_KEY value) to keep the helper pure.
func resolveAuthMethod(cfg *config.Config, apiKeyEnv string) authMethod {
	if strings.TrimSpace(apiKeyEnv) != "" {
		return authPATEnv
	}
	if cfg != nil && strings.TrimSpace(cfg.APIKey) != "" {
		return authPATStored
	}
	if cfg != nil && cfg.RefreshToken != "" && cfg.Firebase != nil {
		return authFirebase
	}
	return authNone
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show CLI status and authentication info",
	Long:  `Display current CLI status including account ID, default cluster, and authentication details.`,
	RunE:  runCLIStatus,
}

func init() {
	statusCmd.Flags().BoolP("json", "j", false, "output as JSON")
}

func runCLIStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")

	status := map[string]any{
		"authenticated": false,
	}

	// Account info
	if cfg.AccountID != "" {
		status["accountId"] = cfg.AccountID
	}

	// URLs
	if cfg.Env != "" {
		status["environment"] = cfg.Env
	}
	if apiURL := cfg.GetAPIURL(); apiURL != "" {
		status["apiUrl"] = apiURL
	}
	if consoleURL := cfg.GetConsoleURL(); consoleURL != "" {
		status["consoleUrl"] = consoleURL
	}

	// Default cluster
	defaultCID := cfg.GetDefaultClusterID()
	if defaultCID != "" {
		status["defaultClusterId"] = defaultCID
	}

	// Check authentication and get token info. A stored PAT or
	// RUNOS_API_KEY counts as authenticated by presence (the same
	// credential ordinary commands use via auth.ResolveToken); only the
	// Firebase path needs a network refresh to confirm the session.
	switch method := resolveAuthMethod(cfg, os.Getenv(auth.APIKeyEnvVar)); method {
	case authPATEnv, authPATStored:
		status["authenticated"] = true
		status["authMethod"] = string(method)
		if cfg.SignedInAt != "" {
			status["signedInAt"] = cfg.SignedInAt
		}
	case authFirebase:
		_, err := auth.RefreshIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
		if err != nil {
			status["authenticated"] = false
			status["authError"] = err.Error()
		} else {
			status["authenticated"] = true
			status["authMethod"] = string(method)

			// Use stored sign-in timestamp
			if cfg.SignedInAt != "" {
				status["signedInAt"] = cfg.SignedInAt
			}
		}
	}

	// Enrich with account profile (company name, website) and the default
	// cluster's name. Best-effort: a network or auth failure just leaves
	// the fields out so `runos status` keeps working offline.
	if authenticated, _ := status["authenticated"].(bool); authenticated {
		if profile := fetchAccountProfile(cfg); profile != nil {
			if profile.CompanyName != "" {
				status["companyName"] = profile.CompanyName
			}
			if profile.Website != "" {
				status["website"] = profile.Website
			}
		}
		if defaultCID != "" {
			if clusters, err := fetchClusters(cfg); err == nil {
				found := false
				for _, c := range clusters {
					if c.CID == defaultCID {
						found = true
						if c.Name != "" {
							status["defaultClusterName"] = c.Name
						}
						break
					}
				}
				// The list fetch succeeded and the cid isn't in it: the
				// configured default is stale (e.g. account switch).
				if !found {
					status["defaultClusterMissing"] = true
				}
			}
		}
	}

	if jsonOutput {
		output, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal status: %w", err)
		}
		fmt.Println(string(output))
		return nil
	}

	// Plain text output
	fmt.Println("RunOS CLI Status")
	fmt.Println("================")
	fmt.Println()

	// Authentication status
	if authenticated, ok := status["authenticated"].(bool); ok && authenticated {
		fmt.Printf("Authentication: ✓ Logged in%s\n", authMethodLabel(status["authMethod"]))
	} else {
		fmt.Println("Authentication: ✗ Not logged in")
		if authErr, ok := status["authError"].(string); ok {
			fmt.Printf("  Error: %s\n", authErr)
		}
	}

	// Account ID
	if accountID, ok := status["accountId"].(string); ok {
		fmt.Printf("Account ID:     %s\n", accountID)
	}

	// Company profile (when set on the account)
	if companyName, ok := status["companyName"].(string); ok {
		fmt.Printf("Company:        %s\n", companyName)
	}
	if website, ok := status["website"].(string); ok {
		fmt.Printf("Website:        %s\n", website)
	}

	// URLs. The environment is deliberately not shown: users are always
	// on prod, so the line was noise (it remains in --json output).
	if apiURL, ok := status["apiUrl"].(string); ok {
		fmt.Printf("API:            %s\n", apiURL)
	}
	if consoleURL, ok := status["consoleUrl"].(string); ok {
		fmt.Printf("Console:        %s\n", consoleURL)
	}

	// Default cluster: "name (cid)" when the name could be resolved,
	// bare cid otherwise.
	if cid, ok := status["defaultClusterId"].(string); ok {
		label := cid
		if name, ok := status["defaultClusterName"].(string); ok {
			label = fmt.Sprintf("%s (%s)", name, cid)
		} else if missing, _ := status["defaultClusterMissing"].(bool); missing {
			label = fmt.Sprintf("%s (not found on this account)", cid)
		}
		fmt.Printf("Default Cluster: %s (change with 'runos config set cid <id>')\n", label)
	} else {
		fmt.Println("Default Cluster: (not set, use 'runos config set cid <id>')")
	}

	// Session info (only show if authenticated)
	if authenticated, ok := status["authenticated"].(bool); ok && authenticated {
		method, _ := status["authMethod"].(string)
		fmt.Printf("\nSession:\n")
		if signedInAt, ok := status["signedInAt"].(string); ok {
			t, err := time.Parse(time.RFC3339, signedInAt)
			if err != nil {
				fmt.Printf("  Signed in:  %s (unparseable)\n", signedInAt)
			} else {
				fmt.Printf("  Signed in:  %s\n", t.Local().Format("2006-01-02 15:04:05"))
			}
		} else if method == string(authPATEnv) || method == string(authPATStored) {
			fmt.Printf("  Signed in:  via personal access token\n")
		} else {
			fmt.Printf("  Signed in:  Unknown (logged in before CLI update)\n")
		}
		fmt.Printf("  Expires:    Never (run 'runos logout' to sign out)\n")
	}

	return nil
}

// accountProfile is the subset of GET /:aid/account/profile that
// `runos status` displays. Unset fields come back as empty strings.
type accountProfile struct {
	CompanyName string `json:"companyName"`
	Website     string `json:"website"`
}

// statusGetJSON performs an authenticated GET against conductor and
// decodes the JSON body into out. Returns an error on any failure so
// callers can treat enrichment as best-effort.
func statusGetJSON(cfg *config.Config, path string, out any) error {
	baseURL := cfg.GetAPIURL()
	if baseURL == "" {
		return fmt.Errorf("no API URL configured")
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (%d)", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// fetchAccountProfile reads the account's company profile. Returns nil
// when the account id is missing or the request fails.
func fetchAccountProfile(cfg *config.Config) *accountProfile {
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil
	}
	var profile accountProfile
	if err := statusGetJSON(cfg, "/"+url.PathEscape(aid)+"/account/profile", &profile); err != nil {
		return nil
	}
	return &profile
}

// statusClusterRow is the cid+name subset of a `/:aid/clusters` row
// that `runos status` needs to label the default cluster.
type statusClusterRow struct {
	CID  string `json:"cid"`
	Name string `json:"name"`
}

// fetchClusters lists the account's clusters (cid + name only).
func fetchClusters(cfg *config.Config) ([]statusClusterRow, error) {
	aid := cfg.GetAccountID()
	if aid == "" {
		return nil, fmt.Errorf("no account id configured")
	}
	var envelope struct {
		Clusters []statusClusterRow `json:"clusters"`
	}
	if err := statusGetJSON(cfg, "/"+url.PathEscape(aid)+"/clusters", &envelope); err != nil {
		return nil, err
	}
	return envelope.Clusters, nil
}

// authMethodLabel renders the parenthetical credential hint for the
// "Logged in" line. Empty for the legacy/unknown case so the line is
// unchanged when no method was recorded.
func authMethodLabel(v any) string {
	switch v {
	case string(authPATEnv):
		return fmt.Sprintf(" (PAT via %s)", auth.APIKeyEnvVar)
	case string(authPATStored):
		return " (PAT)"
	case string(authFirebase):
		return ""
	default:
		return ""
	}
}
