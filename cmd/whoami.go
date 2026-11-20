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
	// Story 343 / FCR2030, finding 1107: "<pat>" named one credential type
	// only (an Account API Key reaches this exact message too), and the
	// example omitted --account-id, which --api-key requires.
	return fmt.Errorf("%w: run 'runos login' (or 'runos login --api-key <credential> --account-id <id>') or set %s", auth.ErrNotAuthenticated, auth.APIKeyEnvVar)
}

// whoamiReport builds the answer. It names credentials by source only
// and never includes a token, key or secret.
//
// "credential_type" is always "unknown" for a secret-sourced credential
// (env or stored): no trusted server response reaches this command
// today that could tell a Personal Access Token from an Account API
// Key, and Story 343 / FCR2030 forbids guessing that from the source or
// a prefix. The field still appears, honestly, rather than being left
// to imply a certainty the CLI does not have.
func whoamiReport(cfg *config.Config, kind auth.CredentialKind, id *roidauth.Identity) map[string]any {
	desc := auth.DescribeSource(kind)
	report := map[string]any{
		"environment": cfg.GetEnv(),
		"credential":  string(kind),
		"account_id":  cfg.GetAccountID(),
	}
	if desc.IsSecretCredential() {
		report["credential_type"] = string(desc.Type)
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
	case auth.CredentialInteractive, auth.CredentialStoredKey:
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
