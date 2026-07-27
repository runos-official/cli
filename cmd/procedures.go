package cmd

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/procedures"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// The Procedure surface is hand-written rather than manifest-driven, for
// four reasons that are all about this surface specifically:
//
//  1. A Procedure's arguments are declared per Procedure and per version.
//     A manifest command declares a fixed field list, and dynacmd's
//     executor REFUSES body-file keys the manifest does not declare
//     (refuseUnknownBodyFileKeys), so there is no manifest shape that
//     carries `runos.valkey.delete`'s arguments and the next Procedure's.
//  2. The decision commands print the full plan and confirm before they
//     send, which under Q&A 131 is the deliberate act that stands in for
//     the credential check that used to be here. dynacmd's generic
//     confirmation is keyed on a verb list, not on a rendered plan.
//  3. MCP tools are generated FROM manifest entries (internal/mcp
//     buildTools). Q&A 131 lets any authenticated caller approve, so
//     absence from the manifest is what still keeps an MCP tool from
//     appearing for `approve` without somebody deciding to add one.
//  4. The approval render is a security object needing a renderer that
//     provably emits no url, token, link, button or image. That is a
//     tested function, not a generic table formatter.

var proceduresCmd = &cobra.Command{
	Use:   "procedures",
	Short: "Deterministic Conductor Procedures: plan, invoke, approve, and stop",
	Long: `Work with Conductor's deterministic Procedure surface.

A Procedure is a stable, immutable-versioned Conductor capability. Invoking one
does not run it: Conductor resolves the targets itself, runs non-waivable
preflight checks, classifies the risk, builds an immutable plan with a hash, and
decides whether a human must approve it before a reconciler executes it.

  runos procedures list                      the catalog
  runos procedures plan <ref> --arg k=v      build and render a plan, persist nothing
  runos procedures run <ref> --arg k=v       create the operation
  runos procedures show <operation-id>       the operation and its approval render
  runos procedures approve <operation-id>    approve, as a freshly signed-in human
  runos procedures reject <operation-id>     reject
  runos procedures revoke <operation-id>     withdraw an approval not yet consumed
  runos procedures kill-switches ...         stop and resume Procedure work
  runos procedures freezes ...               list and release scope freezes

Approving, rejecting and revoking require an account role the Procedure declares.
Any authenticated session may do so, including one using a personal access token.`,
}

var (
	proceduresArgs []string
	proceduresJSON bool
	proceduresYes  bool
	proceduresCid  string
)

var proceduresListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every registered Procedure and its declared arguments",
	Args:  cobra.NoArgs,
	RunE:  runProceduresList,
}

var proceduresPlanCmd = &cobra.Command{
	Use:   "plan <procedure-ref>",
	Short: "Build and render a plan without creating anything",
	Long: `Build a plan for a Procedure and render it. Nothing is persisted: no operation
is created and there is no id to poll.

The reference is the immutable id@version from 'runos procedures list', e.g.
runos.valkey.delete@1.1.0.

  runos procedures plan runos.valkey.clear-cache@1.0.0 --arg osid=valkey-ab2cd --arg pattern='session:*'

A plan the deterministic gates block is reported here with every failing reason,
which is the cheapest way to find out what a Procedure will refuse.`,
	Args: cobra.ExactArgs(1),
	RunE: runProceduresPlan,
}

var proceduresRunCmd = &cobra.Command{
	Use:   "run <procedure-ref>",
	Short: "Create a Procedure operation",
	Long: `Build a plan and, when every deterministic gate allows it, create a durable
operation.

Nothing executes in this command. A Procedure requiring direct caller authority
is authorized by this request and a reconciler runs it; a Procedure requiring a
fresh human waits in pending_authorization until somebody approves it with
'runos procedures approve'.`,
	Args: cobra.ExactArgs(1),
	RunE: runProceduresRun,
}

func init() {
	rootCmd.AddCommand(proceduresCmd)
	proceduresCmd.AddCommand(proceduresListCmd, proceduresPlanCmd, proceduresRunCmd)

	for _, command := range []*cobra.Command{proceduresListCmd, proceduresPlanCmd, proceduresRunCmd} {
		command.Flags().BoolVarP(&proceduresJSON, "json", "j", false, "Output as JSON")
		command.Flags().StringVar(&proceduresCid, "cid", "", "Cluster ID (uses the default from config if not specified)")
	}
	for _, command := range []*cobra.Command{proceduresPlanCmd, proceduresRunCmd} {
		command.Flags().StringArrayVar(&proceduresArgs, "arg", nil,
			"Procedure argument as name=value; repeat for each. Names and types come from 'runos procedures list'")
	}
	proceduresRunCmd.Flags().BoolVarP(&proceduresYes, "yes", "y", false, "Skip the confirmation prompt")
}

// procedureClient loads config and builds the API client. Every
// Procedure command starts here so the credential is resolved once, in
// one place, with its kind attached.
func procedureClient() (*procedures.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return procedures.NewClient(cfg)
}

// procedureCid resolves the cluster from --cid or the configured
// default, refusing rather than guessing when neither is set.
func procedureCid() (string, error) {
	if strings.TrimSpace(proceduresCid) != "" {
		return strings.TrimSpace(proceduresCid), nil
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	// GetDefaultClusterID, not the field: it honours RUNOS_CLUSTER_ID, which is
	// how every other command in this CLI lets a CI runner or a shell pick a
	// cluster without editing config.json. Reading the field directly would
	// make these commands the one surface where that env var silently did
	// nothing.
	if cid := cfg.GetDefaultClusterID(); cid != "" {
		return cid, nil
	}
	return "", fmt.Errorf("no cluster specified; pass --cid <cid>, set %s, or set a default with 'runos clusters default <cid>'", "RUNOS_CLUSTER_ID")
}

func runProceduresList(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	client, err := procedureClient()
	if err != nil {
		return err
	}
	cid, err := procedureCid()
	if err != nil {
		return err
	}
	entries, err := client.Catalog(cid)
	if err != nil {
		return err
	}
	if proceduresJSON {
		return emitJSON(entries)
	}
	fmt.Fprint(cmd.OutOrStdout(), procedures.RenderCatalog(entries))
	return nil
}

func runProceduresPlan(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	return planOrRun(cmd, args[0], true)
}

func runProceduresRun(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	return planOrRun(cmd, args[0], false)
}

// planOrRun is one path for the dry run and the real one because the
// request, the gates and every failure shape are identical; only whether
// a row is written differs. Two copies would drift on exactly the
// blocked-plan rendering that matters most.
func planOrRun(cmd *cobra.Command, ref string, dryRun bool) error {
	client, err := procedureClient()
	if err != nil {
		return err
	}
	cid, err := procedureCid()
	if err != nil {
		return err
	}

	// The catalog read is not decoration: Conductor performs no argument
	// coercion, so the declared types are what turns the terminal's
	// strings into the JSON the spec requires. It also gives the
	// confirmation prompt the Procedure's summary and declared floor.
	entry, err := client.Lookup(cid, ref)
	if err != nil {
		return err
	}
	values, err := procedures.CoerceArgs(entry.Args, proceduresArgs)
	if err != nil {
		return err
	}

	if !dryRun {
		if err := confirmProcedureRun(entry, cid, values); err != nil {
			return err
		}
	}

	outcome, err := client.CreateOperation(cid, ref, values, dryRun)
	if err != nil {
		return err
	}

	switch {
	case outcome.Blocked != nil:
		if proceduresJSON {
			if err := emitJSON(outcome.Blocked); err != nil {
				return err
			}
		} else {
			fmt.Fprint(cmd.OutOrStdout(), procedures.RenderBlocked(outcome.Blocked))
		}
		// A non-zero exit: the gate refused and nothing was created, so a
		// script that only checks the exit code does not read this as a
		// success. The reasons are already on stdout, so the error itself
		// stays short rather than repeating them.
		return fmt.Errorf("blocked by %d deterministic check(s); nothing was created", len(outcome.Blocked.Reasons))

	case outcome.Invalid != nil:
		return fmt.Errorf("Conductor refused the arguments:\n  - %s", strings.Join(outcome.Invalid.Reasons, "\n  - "))

	case outcome.DryRun != nil:
		if proceduresJSON {
			return emitJSON(outcome.DryRun)
		}
		fmt.Fprint(cmd.OutOrStdout(), procedures.RenderPlan(&outcome.DryRun.Plan))
		return nil

	case outcome.Created != nil:
		if proceduresJSON {
			return emitJSON(outcome.Created)
		}
		created := outcome.Created
		fmt.Fprintf(cmd.OutOrStdout(), "Operation %s created.\n", created.OperationID)
		fmt.Fprintf(cmd.OutOrStdout(), "  state          %s\n", created.State)
		fmt.Fprintf(cmd.OutOrStdout(), "  classification %s\n", created.Plan.Classification.Cls)
		fmt.Fprintf(cmd.OutOrStdout(), "  plan hash      %s\n", created.Plan.PlanHash)
		fmt.Fprintf(cmd.OutOrStdout(), "  change id      %s\n\n", created.RootChangeID)
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", created.Note)
		if created.State == "pending_authorization" {
			fmt.Fprintf(cmd.OutOrStdout(), "\nRead the plan:   runos procedures show %s\n", created.OperationID)
			fmt.Fprintf(cmd.OutOrStdout(), "Then decide:     runos procedures approve %s\n", created.OperationID)
		}
		return nil

	default:
		return fmt.Errorf("Conductor returned a response this CLI does not recognise; update with 'runos update'")
	}
}

// confirmProcedureRun asks in the terminal before creating an operation.
//
// The prompt states the Procedure's DECLARED FLOOR rather than a class,
// because the class is computed server-side at plan time and this prompt
// runs before the plan exists. Naming the floor as a floor is the honest
// version; printing it as "risk A2" would be an assertion the CLI cannot
// make.
//
// A non-terminal without --yes REFUSES rather than proceeding silently,
// matching internal/dynacmd's confirmDestructive. Skipping a
// confirmation because nobody is watching is the wrong default on a path
// that creates operations.
func confirmProcedureRun(entry *procedures.CatalogEntry, cid string, values map[string]any) error {
	if proceduresYes {
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf(
			"creating a Procedure operation requires confirmation. Re-run with --yes to proceed (procedure: %s, cluster: %s)",
			entry.Ref, cid)
	}
	// Written to stderr so `runos procedures run ... | jq` still gets
	// clean stdout, which is how confirmDestructive does it too.
	fmt.Fprintf(os.Stderr, "About to create a Procedure operation.\n\n")
	fmt.Fprintf(os.Stderr, "  procedure   %s\n", entry.Ref)
	fmt.Fprintf(os.Stderr, "  summary     %s\n", entry.Summary)
	fmt.Fprintf(os.Stderr, "  cluster     %s\n", cid)
	fmt.Fprintf(os.Stderr, "  risk floor  %s (Conductor may classify the plan HIGHER)\n", entry.RiskFloor)
	fmt.Fprintf(os.Stderr, "  approval    %s\n", entry.Approval)
	if len(values) > 0 {
		fmt.Fprintf(os.Stderr, "  arguments   %s\n", formatArgValues(values))
	}
	fmt.Fprint(os.Stderr, "\nContinue? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
		return fmt.Errorf("cancelled; nothing was created")
	}
	return nil
}

func formatArgValues(values map[string]any) string {
	parts := make([]string, 0, len(values))
	for name, value := range values {
		parts = append(parts, fmt.Sprintf("%s=%v", name, value))
	}
	// Sorted so two runs of the same command print the same line.
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
