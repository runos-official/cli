package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/procedures"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Q&A 130 left open whether the frozen-scope release path needs CLI and
// Console surfaces. It does, and both clients carry all four controls.
// The argument is the design's own, one step further out: "a kill switch
// nobody can throw is not a kill switch" is why the ROUTE exists rather
// than the store being written only by a migration, and a route no
// client calls is thrown by nobody. A scope freeze is stronger still:
// nothing expires one, so authorized human recovery is the only way a
// frozen scope ever unfreezes, and with no surface for it an account
// stays frozen permanently.
//
// The two READS ship alongside because a release command whose id can
// only be obtained by hand-rolling HTTP is not reachable either.

var proceduresKillSwitchesCmd = &cobra.Command{
	Use:   "kill-switches",
	Short: "Stop and resume Procedure work in this account",
	Long: `Engage, list and release Procedure kill switches.

Conductor checks kill switches before it claims an operation and before every
mutating stage.

Engaging is the safe direction and works under a personal access token, because
an incident is exactly when a script needs to be able to stop things. Releasing
restores the ability to mutate and refuses a PAT.`,
}

var proceduresKillSwitchesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List live kill switches in this account",
	Args:  cobra.NoArgs,
	RunE:  runKillSwitchesList,
}

var proceduresKillSwitchesEngageCmd = &cobra.Command{
	Use:   "engage",
	Short: "Engage a kill switch, stopping Procedure work",
	Long: `Stop Procedure work at the scope you name.

  --scope account                                  every Procedure in the account
  --scope cluster --kill-cid <cid>                 every Procedure in one cluster
  --scope procedure --procedure-id <id>            one Procedure across the account
  --scope procedure --procedure-id <id> --kill-cid <cid>   one Procedure in one cluster

A Procedure-scoped switch names an id and never a version, so it stops every
version of that Procedure.

--reason is required: a kill switch with no stated reason is one nobody can
decide to release.

A platform-wide switch is not engageable here. It is a platform decision and
belongs on the root-authenticated administration surface, which this
account-scoped command does not widen.`,
	Args: cobra.NoArgs,
	RunE: runKillSwitchesEngage,
}

var proceduresKillSwitchesReleaseCmd = &cobra.Command{
	Use:   "release <switch-id>",
	Short: "Release a kill switch, restoring the ability to run Procedures",
	Args:  cobra.ExactArgs(1),
	RunE:  runKillSwitchesRelease,
}

var proceduresFreezesCmd = &cobra.Command{
	Use:   "freezes",
	Short: "List and release Procedure scope freezes",
	Long: `A scope freeze is written by Conductor's own verifier when an operation's
postconditions failed or could not be evidenced.

A freeze does not block a scope. It RAISES the action class, and it removes the
direct-caller-authority shortcut so the next operation on that scope asks a
freshly authenticated human instead of running on the caller's own authorization.

Nothing expires a freeze: it records that Conductor does not know what happened,
and time passing does not make it know. Releasing one is the "authorized human
recovery" the contract names, so it is a human and not a credential.`,
}

var proceduresFreezesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List live scope freezes in this account",
	Args:  cobra.NoArgs,
	RunE:  runFreezesList,
}

var proceduresFreezesReleaseCmd = &cobra.Command{
	Use:   "release <freeze-id>",
	Short: "Release a scope freeze",
	Long: `Lift a scope freeze.

A freeze means Conductor could not confirm what an operation did to a target.
Releasing it asserts that a human has established the state, which Conductor
cannot check. Read the freeze's cause first: verification_unverifiable means the
outcome is still unknown rather than known-bad.`,
	Args: cobra.ExactArgs(1),
	RunE: runFreezesRelease,
}

var (
	killSwitchScope       string
	killSwitchCid         string
	killSwitchProcedureID string
	killSwitchReason      string
)

func init() {
	proceduresCmd.AddCommand(proceduresKillSwitchesCmd, proceduresFreezesCmd)
	proceduresKillSwitchesCmd.AddCommand(
		proceduresKillSwitchesListCmd, proceduresKillSwitchesEngageCmd, proceduresKillSwitchesReleaseCmd)
	proceduresFreezesCmd.AddCommand(proceduresFreezesListCmd, proceduresFreezesReleaseCmd)

	for _, command := range []*cobra.Command{proceduresKillSwitchesListCmd, proceduresFreezesListCmd} {
		command.Flags().BoolVarP(&proceduresJSON, "json", "j", false, "Output as JSON")
	}
	engage := proceduresKillSwitchesEngageCmd.Flags()
	engage.StringVar(&killSwitchScope, "scope", "", "account, cluster or procedure (required)")
	// Named --kill-cid rather than --cid: these commands are
	// account-scoped, so a plain --cid would read as "which cluster am I
	// talking to" when it means "which cluster does this switch stop".
	engage.StringVar(&killSwitchCid, "kill-cid", "", "Cluster the switch covers (required for --scope cluster, optional narrowing for --scope procedure)")
	engage.StringVar(&killSwitchProcedureID, "procedure-id", "", "Procedure id the switch covers, without a version (required for --scope procedure)")
	engage.StringVar(&killSwitchReason, "reason", "", "Why you are stopping this (required)")

	for _, command := range []*cobra.Command{proceduresKillSwitchesReleaseCmd, proceduresFreezesReleaseCmd} {
		command.Flags().BoolVarP(&proceduresYes, "yes", "y", false, "Skip the confirmation prompt")
	}
}

func runKillSwitchesList(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	switches, note, err := client.KillSwitchList()
	if err != nil {
		return err
	}
	if proceduresJSON {
		return emitJSON(map[string]any{"data": switches, "note": note})
	}
	out := cmd.OutOrStdout()
	if len(switches) == 0 {
		fmt.Fprintln(out, "No kill switches are engaged in this account.")
	}
	for _, engaged := range switches {
		fmt.Fprintf(out, "\n  %s\n", engaged.SwitchID)
		fmt.Fprintf(out, "    scope     %s\n", killSwitchScopeLine(engaged))
		fmt.Fprintf(out, "    engaged   %s\n", engaged.EngagedAt)
		// Marked as the operator's own words rather than presented as a
		// Conductor fact, and printed exactly as Conductor escaped it.
		fmt.Fprintf(out, "    reason    [operator text] %s\n", engaged.Reason.Text)
	}
	// Always, including under an empty list, which is where it matters:
	// a platform-wide switch stops this account's work and cannot appear
	// in an account-scoped read.
	fmt.Fprintf(out, "\n%s\n", note)
	return nil
}

func killSwitchScopeLine(engaged procedures.KillSwitch) string {
	parts := []string{engaged.Scope}
	if engaged.Cid != nil && *engaged.Cid != "" {
		parts = append(parts, "cluster "+*engaged.Cid)
	}
	if engaged.ProcedureID != nil && *engaged.ProcedureID != "" {
		parts = append(parts, "procedure "+*engaged.ProcedureID+" (every version)")
	}
	return strings.Join(parts, ", ")
}

func runKillSwitchesEngage(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	if strings.TrimSpace(killSwitchScope) == "" {
		return fmt.Errorf("--scope is required: account, cluster or procedure")
	}
	if strings.TrimSpace(killSwitchReason) == "" {
		return fmt.Errorf("--reason is required; a kill switch with no stated reason is one nobody can decide to release")
	}
	client, err := procedureClient()
	if err != nil {
		return err
	}
	switchID, err := client.KillSwitchEngage(
		strings.TrimSpace(killSwitchScope),
		strings.TrimSpace(killSwitchCid),
		strings.TrimSpace(killSwitchProcedureID),
		killSwitchReason)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Kill switch %s engaged.\n", switchID)
	fmt.Fprintf(cmd.OutOrStdout(), "Procedure work in this scope now stops before it is claimed and before every mutating stage.\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Release it with: runos procedures kill-switches release %s\n", switchID)
	return nil
}

func runKillSwitchesRelease(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	// Releasing restores the ability to mutate, so it is the half a
	// stored secret must not reach. Engaging deliberately has no such
	// refusal.
	if err := client.RefuseStoredSecret("release a Procedure kill switch"); err != nil {
		return err
	}
	if err := confirmControlRelease("kill switch", args[0],
		"Procedure work stopped by this switch becomes runnable again."); err != nil {
		return err
	}
	if err := client.KillSwitchRelease(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Kill switch %s released.\n", args[0])
	return nil
}

// confirmControlRelease asks before either release, because both are the
// direction that restores the ability to mutate. Same shape as every
// other confirmation here: a non-terminal without --yes refuses rather
// than proceeding unwatched.
func confirmControlRelease(kind, id, consequence string) error {
	if proceduresYes {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("releasing a %s requires confirmation. Re-run with --yes to proceed (id: %s)", kind, id)
	}
	fmt.Fprintf(os.Stderr, "About to release %s %s.\n\n%s\n", kind, id, consequence)
	fmt.Fprint(os.Stderr, "\nProceed? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return fmt.Errorf("cancelled; %s %s is still in force", kind, id)
	}
	return nil
}

func runFreezesList(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	freezes, err := client.FreezeList()
	if err != nil {
		return err
	}
	if proceduresJSON {
		return emitJSON(map[string]any{"data": freezes})
	}
	out := cmd.OutOrStdout()
	if len(freezes) == 0 {
		fmt.Fprintln(out, "No scope freezes are in force in this account.")
		return nil
	}
	for _, freeze := range freezes {
		fmt.Fprintf(out, "\n  %s\n", freeze.FreezeID)
		fmt.Fprintf(out, "    scope      %s\n", freeze.ScopeKey)
		fmt.Fprintf(out, "    cause      %s\n", freeze.Cause)
		fmt.Fprintf(out, "    reason     %s\n", freeze.Reason)
		fmt.Fprintf(out, "    operation  %s\n", freeze.OperationID)
		fmt.Fprintf(out, "    since      %s\n", freeze.FrozenAt)
	}
	fmt.Fprintf(out, "\nNothing expires a freeze. Release one with: runos procedures freezes release <freeze-id>\n")
	return nil
}

func runFreezesRelease(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	if err := client.RefuseStoredSecret("release a Procedure scope freeze"); err != nil {
		return err
	}
	if err := confirmControlRelease("scope freeze", args[0],
		"A freeze means Conductor could not confirm what an operation did to this target.\n"+
			"Releasing it asserts that a human has established the state, which Conductor cannot check."); err != nil {
		return err
	}
	if err := client.FreezeRelease(args[0]); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Scope freeze %s released.\n", args[0])
	return nil
}
