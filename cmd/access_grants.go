package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var grantFields = []string{"id", "subject", "role", "actions", "scope", "expires_at", "granted_by"}

var accessGrantsCmd = &cobra.Command{Use: "grants", Short: "List, create and withdraw grants, and read a grant's audit trail"}

var accessGrantsListCmd = &cobra.Command{
	Use: "list", Short: "List grants, optionally filtered", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		q := url.Values{}
		for _, f := range []string{"subject", "role", "scope", "under", "cursor"} {
			if v, _ := cmd.Flags().GetString(f); v != "" {
				switch f {
				case "scope", "under":
					if v, err = accessScope(t.acct, v); err != nil {
						return err
					}
				case "subject":
					if err := validateListSubjectFilter(v); err != nil {
						return err
					}
				}
				q.Set(f, v)
			}
		}
		if n, _ := cmd.Flags().GetInt("limit"); n > 0 {
			q.Set("limit", strconv.Itoa(n))
		}
		res, err := t.do(roidapi.Call{Op: "listGrants", Query: q})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("array", grantFields...))
	},
}

var accessGrantsShowCmd = &cobra.Command{
	Use: "show <grant>", Short: "Show one grant", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "getGrant", Params: map[string]string{"grant": args[0]}})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("object", append(grantFields, "conditions", "not_before", "version")...))
	},
}

var accessGrantsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Grant a role, or a list of actions, at a scope",
	Long: `Grant a role (--role) or a list of actions (--action) to a person,
group or legacy key (--subject kind:id), or to an Account API Key
(--key <public-id>), at a scope (--scope, default the whole account; a
short form such as cluster/c1 is placed under the account).

--key takes the key's public id only (never its secret) and always grants
explicit actions: an Account API Key cannot receive a role. Granting to a
key needs an explicit --account; the CLI never assumes the active account
for a machine credential, because its grants can live in any account.

An Account API Key is created with 'runos account account-api-keys add' or in the Console.
Creating this credential needs a registered security key. If you have none, add one on your MFA page first.

You can only hand out actions you hold yourself. Handing out sensitive
actions needs a security-key confirmation.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		var f grantFlags
		f.subject, _ = cmd.Flags().GetString("subject")
		f.keyID, _ = cmd.Flags().GetString("key")
		f.role, _ = cmd.Flags().GetString("role")
		f.actions, _ = cmd.Flags().GetStringSlice("action")
		f.scope, _ = cmd.Flags().GetString("scope")
		f.sourceRanges, _ = cmd.Flags().GetStringSlice("source-range")
		f.window, _ = cmd.Flags().GetString("window")
		f.requireMFA, _ = cmd.Flags().GetBool("require-mfa")
		f.minAssurance, _ = cmd.Flags().GetString("min-assurance")
		f.notBefore, _ = cmd.Flags().GetString("not-before")
		f.expires, _ = cmd.Flags().GetString("expires")
		f.expiresIn, _ = cmd.Flags().GetDuration("expires-in")
		account, _ := cmd.Flags().GetString("account")
		if err := requireExplicitAccountForAKey(f.keyID, account); err != nil {
			return err
		}
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		body, err := grantBody(t.acct, f, time.Now())
		if err != nil {
			return err
		}
		res, err := t.do(writeCall(cmd, roidapi.Call{Op: "createGrant", Body: body}))
		if err != nil {
			return err
		}
		return t.print(res, outputFields("object", grantFields...))
	},
}

var accessGrantsWithdrawCmd = &cobra.Command{
	Use:     "withdraw <grant>",
	Aliases: []string{"revoke"},
	Short:   "Withdraw a grant from this account (local only; never revokes the credential itself)",
	Long: `Withdraw one grant from this account only. Always needs an explicit
--account: the CLI never assumes the active account for a grant mutation.

This is a LOCAL operation: it removes this account's own grant. It does
not change the credential's lifecycle state (expiry, global revocation)
and does not touch its grants in any other account.

To end a typed Account API Key or Personal Access Token everywhere, its
own global revoke command is 'runos account account-api-keys revoke' or
'runos account personal-access-tokens revoke' -- not yet published while
Conductor holds the typed lifecycle commands back. 'runos account
api-keys revoke' is the separate LEGACY command: it only reaches a
pre-existing legacy row, never a typed credential.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		account, _ := cmd.Flags().GetString("account")
		if err := requireExplicitAccount(account); err != nil {
			return err
		}
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		if _, err := t.do(writeCall(cmd, roidapi.Call{Op: "revokeGrant", Params: map[string]string{"grant": args[0]}})); err != nil {
			return err
		}
		return t.done(fmt.Sprintf("Withdrew grant %s from account %s. This does not change the credential's lifecycle state or its grants in any other account.", args[0], t.acct))
	},
}

var accessGrantsAuditCmd = &cobra.Command{
	Use: "audit <grant>", Short: "Show the audit trail of one grant, oldest first", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		t, err := accessSession(cmd)
		if err != nil {
			return err
		}
		res, err := t.do(roidapi.Call{Op: "getGrantAudit", Params: map[string]string{"grant": args[0]}})
		if err != nil {
			return err
		}
		return t.print(res, outputFields("array", "seq", "at", "event", "actor", "actor_class"))
	},
}

func init() {
	l := accessGrantsListCmd.Flags()
	l.String("subject", "", "Only grants to this subject (person:<id>, group:<id>, key:<id> or account_api_key:<public-id>)")
	l.String("role", "", "Only grants of this role")
	l.String("scope", "", "Only grants at exactly this scope")
	l.String("under", "", "Grants at this scope and every scope below it")
	l.Int("limit", 0, "Page size (1 to 200)")
	l.String("cursor", "", "The next_cursor of the previous page")

	c := accessGrantsCreateCmd.Flags()
	c.String("subject", "", "Who gets it: person:<id>, group:<id> or key:<id>")
	c.String("key", "", "Grant to an Account API Key by its public id (never a secret); not with --subject")
	c.String("role", "", "A role id")
	c.StringSlice("action", nil, "An action id instead of a role (repeatable)")
	c.String("scope", "", "Where it applies (default: the whole account)")
	c.StringSlice("source-range", nil, "Only from this CIDR prefix (repeatable)")
	c.String("window", "", "Only in this daily window, HH:MM-HH:MM")
	c.Bool("require-mfa", false, "Only for a sign-in with a second factor")
	c.String("min-assurance", "", "Minimum sign-in strength: password, otp, passkey or hardware")
	c.String("not-before", "", "Starts at this time (RFC 3339)")
	c.String("expires", "", "Ends at this time (RFC 3339)")
	c.Duration("expires-in", 0, "Ends after this long, for example 8h")
	writeFlags(accessGrantsCreateCmd, false)
	writeFlags(accessGrantsWithdrawCmd, true)

	accessGrantsCmd.AddCommand(accessGrantsListCmd, accessGrantsShowCmd, accessGrantsCreateCmd, accessGrantsWithdrawCmd, accessGrantsAuditCmd)
}
