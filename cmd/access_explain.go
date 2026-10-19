package cmd

import (
	"net/url"

	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var accessExplainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Show what a person can do at a scope, and why",
	Long: `Show every action that applies at a scope for a person: whether it is
permitted, needs something more (a step-up, a fresh sign-in, an approval)
or is denied, and the grant and group that decided it. You can always ask
about yourself; asking about someone else needs access to read the
account's access configuration.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		person, _ := cmd.Flags().GetString("person")
		scopeFlag, _ := cmd.Flags().GetString("scope")
		scope, err := accessScope(t.acct, scopeFlag)
		if err != nil {
			return err
		}
		q := url.Values{"person": {person}, "scope": {scope}}
		if a, _ := cmd.Flags().GetString("action"); a != "" {
			q.Set("action", a)
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
	_ = accessExplainCmd.MarkFlagRequired("person")
	accessExplainCmd.Flags().String("scope", "", "The scope (default: the whole account)")
	accessExplainCmd.Flags().String("action", "", "Only this action")
}
