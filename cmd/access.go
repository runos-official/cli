package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/output"
	"github.com/runos-official/cli/internal/roidapi"

	"github.com/spf13/cobra"
)

var accessCmd = &cobra.Command{
	Use:   "access",
	Short: "Manage who can do what: roles, groups and grants",
	Long: `Manage access to this account through the RunOS identity service:
roles (sets of actions), groups (sets of people), grants (a role or actions
for a person, group or machine key at a scope), and explain (what a person
can do at a scope, and why).

These commands talk to the identity service directly, not to conductor.
They need a sign-in through the identity service ('runos login').

Changes to an existing role, group or grant are safe against concurrent
edits: the CLI reads the current version first and the change fails if it
changed in between. A change that hands out sensitive actions needs a
security-key confirmation; the CLI says how.`,
}

func init() {
	accessCmd.PersistentFlags().String("account", "", "Account id (default: the current account)")
	accessCmd.PersistentFlags().BoolP("json", "j", false, "Print the answer as JSON")
	accessCmd.AddCommand(accessRolesCmd, accessGroupsCmd, accessGrantsCmd, accessExplainCmd, accessActionsCmd)
}

// hideAccessUnlessRoid keeps 'runos access' out of the help of an environment
// that signs in through Firebase, where every access command refuses. The
// command still runs there and says why it refuses.
func hideAccessUnlessRoid() {
	cfg, err := config.LoadLocal() // no network at start-up
	accessCmd.Hidden = err != nil || cfg.AuthIssuer == ""
}

// accessTarget is what every access command needs: a client for the
// identity service and the account it acts on.
type accessTarget struct {
	client *roidapi.Client
	acct   string
	json   bool
}

func accessSession(cmd *cobra.Command) (*accessTarget, error) {
	cmd.SilenceUsage = true
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	base, err := roidapi.BaseURL(cfg)
	if err != nil {
		return nil, err
	}
	if auth.Kind(cfg) != auth.CredentialRoid {
		return nil, fmt.Errorf("%w: access commands need a sign-in through the RunOS identity service, run 'runos login'", auth.ErrNotAuthenticated)
	}
	acct, _ := cmd.Flags().GetString("account")
	if acct == "" {
		acct = cfg.GetAccountID()
	}
	if acct == "" {
		return nil, errors.New("no account selected: pass --account or run 'runos account switch'")
	}
	jsonOut, _ := cmd.Flags().GetBool("json")
	token := func() (string, error) { return auth.ResolveToken(cfg) }
	return &accessTarget{client: roidapi.New(base, token), acct: acct, json: jsonOut}, nil
}

// do sends one call for this account and turns a refusal into an error a
// person can act on.
func (t *accessTarget) do(call roidapi.Call) (*api.Result, error) {
	if call.Params == nil {
		call.Params = map[string]string{}
	}
	call.Params["acct"] = t.acct
	res, err := t.client.DoConfirmed(call, stepUpPrompt(os.Stderr, openBrowser), roidapi.DefaultStepUpWait())
	if err != nil {
		return nil, accessRefusal(err)
	}
	return res, nil
}

// stepUpPrompt tells the person to confirm a change with their security
// key on the identity service's page, and opens it in a browser. It
// writes to stderr, so --json output stays clean.
func stepUpPrompt(w io.Writer, open func(string) error) func(*roidapi.StepUpRequiredError) {
	return func(su *roidapi.StepUpRequiredError) {
		fmt.Fprintf(w, "This change needs a confirmation with your security key.\nOpen %s to confirm it. Waiting...\n", su.URL)
		if err := open(su.URL); err != nil {
			fmt.Fprintln(w, "(Could not open a browser: open the link yourself.)")
		}
	}
}

// print renders an answer as a table (def set) or JSON (--json).
func (t *accessTarget) print(res *api.Result, def *manifest.Output) error {
	return output.NewFormatter(t.json).Format(res.Body, def)
}

// done prints a one-line outcome for a change that answers no body.
func (t *accessTarget) done(msg string) error {
	if t.json {
		fmt.Printf("{\"ok\":true,\"message\":%q}\n", msg)
		return nil
	}
	fmt.Println(msg)
	return nil
}

// accessRefusal explains a refusal. A step-up names the flag to repeat the
// change with; a stale change keeps its kind for errors.Is.
func accessRefusal(err error) error {
	var su *roidapi.StepUpRequiredError
	if errors.As(err, &su) {
		if su.URL != "" {
			return fmt.Errorf("%w. Confirm it with your security key at %s, then repeat the same command with --step-up %s", err, su.URL, su.ID)
		}
		return fmt.Errorf("%w. Confirm it with your security key in the RunOS Console, then repeat the same command with --step-up %s", err, su.ID)
	}
	return err
}

// writeFlags adds the flags every change takes.
func writeFlags(c *cobra.Command, etag bool) {
	c.Flags().String("step-up", "", "A completed security-key step-up id, to repeat a change that asked for one")
	if etag {
		c.Flags().String("if-match", "", "Change only this version (an ETag from 'show'); default: the version read just before the change")
	}
}

// writeCall fills the step-up and If-Match flags into a call.
func writeCall(cmd *cobra.Command, call roidapi.Call) roidapi.Call {
	call.StepUp, _ = cmd.Flags().GetString("step-up")
	if f := cmd.Flags().Lookup("if-match"); f != nil {
		call.IfMatch = f.Value.String()
	}
	return call
}

func outputFields(kind string, names ...string) *manifest.Output {
	def := &manifest.Output{Type: kind}
	for _, n := range names {
		def.Fields = append(def.Fields, manifest.OutputField{Name: n})
	}
	return def
}
