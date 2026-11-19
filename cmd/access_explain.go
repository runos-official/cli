package cmd

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var accessExplainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Show what a person, Personal Access Token or Account API Key can do, and why",
	Long: `Show what a person, a Personal Access Token or an Account API Key can do,
and why. Give --person or --credential, not both.

--person shows every action that applies at a scope: whether each is
permitted, needs something more (a step-up, a fresh sign-in, an approval)
or is denied, and the grant and group that decided it. You can always ask
about yourself; asking about someone else needs access to read the
account's access configuration.

--credential explains one exact action and scope for a Personal Access
Token or Account API Key, using its current public id (pat_... or
key_...), never its secret: a value shaped like a secret is refused
before anything is sent. It always needs an explicit --account (a
credential's authority or grants can differ by account, so the CLI never
assumes the active one) and an explicit --action. The answer is the real,
current decision: a conditional, not-yet-determined, or legacy
compatibility result never prints as an unconditional allow.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		person, _ := cmd.Flags().GetString("person")
		credential, _ := cmd.Flags().GetString("credential")
		action, _ := cmd.Flags().GetString("action")
		if err := validateExplainSelector(person, credential, action, cmd.Flags().Changed("account")); err != nil {
			return err
		}
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		scopeFlag, _ := cmd.Flags().GetString("scope")
		scope, err := accessScope(t.acct, scopeFlag)
		if err != nil {
			return err
		}
		if credential != "" {
			resp, res, err := t.explainCredential(credential, action, scope)
			if err != nil {
				return err
			}
			if t.json {
				return t.print(res, nil)
			}
			renderExplainCredential(os.Stdout, *resp)
			return nil
		}
		q := url.Values{"person": {person}, "scope": {scope}}
		if action != "" {
			q.Set("action", action)
		}
		res, err := t.do(roidapi.Call{Op: "getEffectiveAccess", Query: q})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("array", "action", "class", "outcome", "needs", "grant", "via", "reason"))
	},
}

var accessActionsCmd = &cobra.Command{
	Use: "actions", Short: "List the actions roles and grants can name", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "listActions"})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("array", "id", "class", "scope_type", "module", "escalation"))
	},
}

func init() {
	accessExplainCmd.Flags().String("person", "", "The person id")
	accessExplainCmd.Flags().String("credential", "", "A Personal Access Token or Account API Key's public id (pat_... or key_...), never a secret")
	accessExplainCmd.Flags().String("scope", "", "The scope (default: the whole account)")
	accessExplainCmd.Flags().String("action", "", "Only this action (required with --credential)")
}

// validateExplainSelector checks the local input for 'access explain' before
// any request is built: exactly one subject (a person or a credential), a
// credential that is a public id rather than a secret, an explicit
// --account for a credential (its authority or grants can differ by
// account, so the CLI never assumes the active one, unlike the person
// path), and an action to evaluate (the credential request requires one;
// the person path may ask about every applicable action).
func validateExplainSelector(person, credential, action string, accountGiven bool) error {
	switch {
	case person != "" && credential != "":
		return errors.New("give --person or --credential, not both")
	case person == "" && credential == "":
		return errors.New("give --person or --credential: who or what is this explaining?")
	}
	if credential == "" {
		return nil
	}
	if err := rejectSecretShapedCredential(credential); err != nil {
		return err
	}
	if !accountGiven {
		return errors.New("explaining a credential needs --account: its authority or grants can differ by account, so the CLI never assumes the active one")
	}
	if action == "" {
		return errors.New("explaining a credential needs --action: one exact action to evaluate")
	}
	return nil
}

// secretShapedCredentialPrefixes are the shapes of an actual bearer secret,
// never a public credential id (docs/api/scoped-credentials.yaml in the
// roid repo: the PublicId schema excludes exactly these). Catching this
// before a request is built keeps a pasted secret out of the request and
// out of any error about it.
var secretShapedCredentialPrefixes = []string{"runos_pat_", "runos_key_", "rk_"}

func rejectSecretShapedCredential(v string) error {
	for _, p := range secretShapedCredentialPrefixes {
		if strings.HasPrefix(v, p) {
			return errors.New("--credential takes a public id (pat_... or key_...), never a secret: use the id the identity service shows for it, not the token itself")
		}
	}
	return nil
}
