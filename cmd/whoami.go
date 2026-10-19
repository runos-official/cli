package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/output"
	"github.com/runos-official/cli/internal/roidauth"

	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show who this CLI is signed in as",
	Long: `Show the credential this CLI sends and who it signs in as.

For an environment that signs in through the RunOS identity service, the
person and their accounts come from the service, so the answer is live.
For other credentials the answer is what this machine's config records.`,
	RunE: runWhoami,
}

func init() {
	whoamiCmd.Flags().BoolP("json", "j", false, "Print the answer as JSON")
}

func whoamiNotSignedIn() error {
	return fmt.Errorf("%w: run 'runos login' (or 'runos login --api-key <pat>') or set %s", auth.ErrNotAuthenticated, auth.APIKeyEnvVar)
}

// whoamiReport builds the answer. It names credentials by kind only and
// never includes a token, key or PAT.
func whoamiReport(cfg *config.Config, kind auth.CredentialKind, id *roidauth.Identity) map[string]any {
	report := map[string]any{
		"environment": cfg.GetEnv(),
		"credential":  string(kind),
		"account_id":  cfg.GetAccountID(),
	}
	switch kind {
	case auth.CredentialRoid:
		report["auth_issuer"] = cfg.AuthIssuer
		if cfg.Roid != nil {
			report["signed_in_at"] = cfg.Roid.SignedInAt
		}
		if id != nil {
			report["person_id"] = id.PersonID
			report["method"] = id.Method
			report["assurance"] = id.Assurance
			report["accounts"] = id.Accounts
		}
	case auth.CredentialInteractive, auth.CredentialStoredPAT:
		report["signed_in_at"] = cfg.SignedInAt
	}
	return report
}

func runWhoami(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	kind := auth.Kind(cfg)
	if kind == auth.CredentialNone {
		return whoamiNotSignedIn()
	}
	var id *roidauth.Identity
	if kind == auth.CredentialRoid {
		s, err := auth.RoidSession(cfg)
		if err != nil {
			return err
		}
		live, err := s.Identity()
		if err != nil {
			return err
		}
		id = &live
	}
	body, err := json.Marshal(whoamiReport(cfg, kind, id))
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	return output.NewFormatter(jsonOutput).Format(body, nil)
}
