package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"

	"github.com/spf13/cobra"
)

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

	// Check authentication and get token info
	if cfg.RefreshToken != "" && cfg.Firebase != nil {
		_, err := auth.RefreshIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
		if err != nil {
			status["authenticated"] = false
			status["authError"] = err.Error()
		} else {
			status["authenticated"] = true

			// Use stored sign-in timestamp
			if cfg.SignedInAt != "" {
				status["signedInAt"] = cfg.SignedInAt
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
		fmt.Println("Authentication: ✓ Logged in")
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

	// Environment and URLs
	if env, ok := status["environment"].(string); ok {
		fmt.Printf("Environment:    %s\n", env)
	}
	if apiURL, ok := status["apiUrl"].(string); ok {
		fmt.Printf("API:            %s\n", apiURL)
	}
	if consoleURL, ok := status["consoleUrl"].(string); ok {
		fmt.Printf("Console:        %s\n", consoleURL)
	}

	// Default cluster
	if cid, ok := status["defaultClusterId"].(string); ok {
		fmt.Printf("Default Cluster: %s (change with 'runos config set default-cluster <id>')\n", cid)
	} else {
		fmt.Println("Default Cluster: (not set, use 'runos config set default-cluster <id>')")
	}

	// Session info (only show if authenticated)
	if authenticated, ok := status["authenticated"].(bool); ok && authenticated {
		fmt.Printf("\nSession:\n")
		if signedInAt, ok := status["signedInAt"].(string); ok {
			t, err := time.Parse(time.RFC3339, signedInAt)
			if err != nil {
				fmt.Printf("  Signed in:  %s (unparseable)\n", signedInAt)
			} else {
				fmt.Printf("  Signed in:  %s\n", t.Local().Format("2006-01-02 15:04:05"))
			}
		} else {
			fmt.Printf("  Signed in:  Unknown (logged in before CLI update)\n")
		}
		fmt.Printf("  Expires:    Never (run 'runos logout' to sign out)\n")
	}

	return nil
}
