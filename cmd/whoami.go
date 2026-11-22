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
For other credentials the answer is what this machine's config records.

For a secret credential (RUNOS_API_KEY or a key stored by 'runos login
--api-key'), this command also asks the server whether it accepts the
credential, and reports one of three states in "verified" /
"verified_reason":
  - accepted: the server accepted the credential. The account and role
    it returned are printed. credential_type and credential_id report
    the server's own verified name for the credential when it names
    one; otherwise credential_type stays "unknown" and credential_id is
    not printed. For a Personal Access Token or an Account API Key, the
    server may also deliberately not look accounts up: then "accounts"
    is empty and accounts_omitted names why, with one plain sentence in
    accounts_note saying the account list is not shown for this
    credential type, so the empty list never reads as "member of no
    account".
  - human_sign_in_required: the server answered, but the check this
    command makes needs a human sign-in; the credential itself was not
    judged.
  - unreachable: the server could not be reached; the credential itself
    was not judged.
Exit codes: exit 0 for accepted, human_sign_in_required and
unreachable, because the command answered truthfully even when nothing
judged the credential; exit 1 only for a 401 refusal of the credential
itself.`,
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
// "credential_type" defaults to "unknown" for a secret-sourced
// credential (env or stored): Story 343 / FCR2030 forbids guessing the
// type from the source or a prefix. runWhoami's merge of
// checkSecretCredential's fields overwrites this default only when
// conductor's own response named the credential (contract C-4, story
// 439); otherwise it stays "unknown", honestly, rather than implying a
// certainty the CLI does not have.
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
	report := whoamiReport(cfg, kind, id)
	if kind.IsSecretCredential() {
		extra, err := checkSecretCredential(cfg, kind)
		if err != nil {
			return err
		}
		for k, v := range extra {
			report[k] = v
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	return output.NewFormatter(jsonOutput).Format(body, nil)
}
