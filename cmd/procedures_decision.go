package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/procedures"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var proceduresShowCmd = &cobra.Command{
	Use:   "show <operation-id>",
	Short: "Show an operation and the full approval render",
	Args:  cobra.ExactArgs(1),
	RunE:  runProceduresShow,
}

var proceduresApproveCmd = &cobra.Command{
	Use:   "approve <operation-id>",
	Short: "Approve a plan, as a freshly signed-in human",
	Long: `Approve the exact plan Conductor renders, binding the decision to its plan hash.

Any authenticated session may approve, provided it holds an account role the
Procedure declares. A personal access token decides exactly as an interactive
login does.

The plan is printed in full and confirmed in the terminal before anything is
sent. --yes skips that confirmation, so a script approves without a prompt;
it does not skip the role check, which is the server's and is not waivable here.

What no approval can do, whatever the credential: waive a deterministic check
that failed or could not be evaluated.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return runProceduresDecide(cmd, args[0], "approve") },
}

var proceduresRejectCmd = &cobra.Command{
	Use:   "reject <operation-id>",
	Short: "Reject a plan, as a freshly signed-in human",
	Long: `Reject the exact plan Conductor renders.

A rejection is a decision and is recorded as one, so it carries the same
requirement as an approval: a role the Procedure declares.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return runProceduresDecide(cmd, args[0], "reject") },
}

var proceduresRevokeCmd = &cobra.Command{
	Use:   "revoke <operation-id>",
	Short: "Withdraw an approval the reconciler has not consumed",
	Long: `Withdraw an authorization that has already been granted and not yet consumed.

Rejecting and revoking are both "no" and they are not the same no: a rejection is
the first answer, a revocation withdraws an answer already given.

The same principals that may approve may revoke, which under Q&A 131 is any
authenticated session holding a role the Procedure declares. Two rules would let
a credential grant an authorization it could not then take back.`,
	Args: cobra.ExactArgs(1),
	RunE: runProceduresRevoke,
}

func init() {
	proceduresCmd.AddCommand(proceduresShowCmd, proceduresApproveCmd, proceduresRejectCmd, proceduresRevokeCmd)

	proceduresShowCmd.Flags().BoolVarP(&proceduresJSON, "json", "j", false, "Output as JSON")
	proceduresShowCmd.Flags().StringVar(&proceduresCid, "cid", "", "Cluster ID (uses the default from config if not specified)")
	for _, command := range []*cobra.Command{proceduresApproveCmd, proceduresRejectCmd, proceduresRevokeCmd} {
		command.Flags().StringVar(&proceduresCid, "cid", "", "Cluster ID (uses the default from config if not specified)")
		command.Flags().BoolVarP(&proceduresYes, "yes", "y", false, "Skip the terminal confirmation (does not skip any authorization requirement)")
	}
}

func runProceduresShow(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	cid, err := procedureCid()
	if err != nil {
		return err
	}
	operation, err := client.Operation(cid, args[0])
	if err != nil {
		return err
	}
	if proceduresJSON {
		return emitJSON(operation)
	}
	fmt.Fprint(cmd.OutOrStdout(), procedures.RenderApproval(operation))
	return nil
}

// runProceduresDecide is approve and reject in one function because they
// are the same act with a different answer: the same route, the same
// four authorization checks, the same plan-hash binding and the same
// freshness rule. Two copies would let one of them drift into accepting
// something the other refuses, which is precisely the defect worth
// designing against here.
func runProceduresDecide(cmd *cobra.Command, operationID, decision string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	// NO CREDENTIAL REFUSAL HERE, under Q&A 131, which supersedes Q&A 120.
	// A PAT, an API key and an interactively signed-in session all decide
	// alike; a developer running this CLI under a PAT is a person at a
	// keyboard. The deliberate act is the confirmation below, not the
	// credential. The role check is the server's and is unchanged.
	cid, err := procedureCid()
	if err != nil {
		return err
	}

	operation, err := client.Operation(cid, operationID)
	if err != nil {
		return err
	}
	if operation.ApprovalRequest == nil {
		return fmt.Errorf("this operation has no approval render, so there is no plan to decide on: %s", operation.Note)
	}
	// THE HASH COMES FROM THE RENDER THAT WAS JUST PRINTED, and from
	// nowhere else. Conductor refuses a decision naming a different plan;
	// re-fetching a hash after a mismatch and resubmitting it would
	// approve a plan nobody read, which is the one thing this binding
	// exists to prevent.
	planHash := operation.ApprovalRequest.PlanHash

	fmt.Fprint(cmd.OutOrStdout(), procedures.RenderApproval(operation))
	if err := confirmDecision(operation, decision); err != nil {
		return err
	}

	decided, result, err := client.Decide(cid, operationID, decision, planHash)
	if err != nil {
		return decisionError(err, result)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nOperation %s is %s.\n", decided.OperationID, decided.State)
	if decided.State == "authorized" {
		fmt.Fprintf(cmd.OutOrStdout(), "The authorization is single-use and expires at %s if the reconciler has not consumed it.\n", decided.ExpiresAt)
		fmt.Fprintf(cmd.OutOrStdout(), "Withdraw it while it is unconsumed with: runos procedures revoke %s\n", decided.OperationID)
	}
	return nil
}

func runProceduresRevoke(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	cid, err := procedureCid()
	if err != nil {
		return err
	}
	operation, err := client.Operation(cid, args[0])
	if err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), procedures.RenderApproval(operation))
	if err := confirmDecision(operation, "revoke the authorization for"); err != nil {
		return err
	}
	revoked, err := client.Revoke(cid, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nOperation %s is %s.\n", revoked.OperationID, revoked.State)
	return nil
}

// confirmDecision asks in the terminal, after the plan has been printed.
//
// A non-terminal without --yes refuses, like every other confirmation in
// this CLI. --yes skips THIS prompt and nothing else: the login, the
// freshness and the role checks are Conductor's and are not waivable
// from here, which the approve command's help says in as many words.
func confirmDecision(operation *procedures.Operation, decision string) error {
	if proceduresYes {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf(
			"deciding a Procedure operation requires confirmation. Re-run with --yes to proceed (operation: %s)",
			operation.OperationID)
	}
	fmt.Fprintf(os.Stderr, "\nYou are about to %s the plan above.\n", decision)
	fmt.Fprintf(os.Stderr, "  operation      %s\n", operation.OperationID)
	fmt.Fprintf(os.Stderr, "  classification %s\n", operation.Classification)
	if operation.ApprovalRequest != nil {
		fmt.Fprintf(os.Stderr, "  plan hash      %s\n", operation.ApprovalRequest.PlanHash)
	}
	fmt.Fprint(os.Stderr, "\nProceed? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return fmt.Errorf("cancelled; no decision was recorded")
	}
	return nil
}

// decisionError adds the recovery path for the refusals a user can
// actually act on, keeping Conductor's own wording underneath.
//
// The freshness case is the one worth naming: a refreshed token does NOT
// carry a newer authentication instant, so retrying is guaranteed to be
// refused identically. Signing in again is the only thing that changes
// the answer, and a message that did not say so would leave a user
// retrying a command that cannot start working.
func decisionError(err error, result *api.Result) error {
	switch procedures.DecisionCode(result) {
	case "role_not_authorized", "membership_recheck_failed":
		return fmt.Errorf("%w\n\n"+
			"An account owner or admin must grant you a role this Procedure declares. The role is\n"+
			"rechecked against the account directory at decision time, so a role granted just now\n"+
			"takes effect on your next attempt", err)
	default:
		return err
	}
}
