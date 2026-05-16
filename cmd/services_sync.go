package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/jobs"
	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var servicesSyncCmd = &cobra.Command{
	Use:   "sync [yaml-file]",
	Short: "Push a local service yaml to the cluster",
	Long: `Sync a local runos.service.<cid>.<sid>.yaml back to the cluster. The
reverse of 'runos services pull'.

Two modes, picked from the yaml's id field:

  - id present: PATCH /services/<type>/<id>. Sends every field the local
                yaml has that the manifest's update endpoint accepts.
                Conductor's per-type omit-equals-preserve / omit-equals-clear
                rules apply on the server side; immutable-after-create
                fields surface as "refused" in the plan output.
  - id absent:  POST /services/<type>. Provisions a new service. The new
                id is written back to the yaml on success.

The yaml schema is derived from the conductor manifest at runtime, so
new service types and new fields flow through without a CLI change.

Examples:
  runos services sync runos.service.mycluster3.mjn1d.yaml --dry-run
  runos services sync runos.service.mycluster3.mjn1d.yaml`,
	Args: cobra.MaximumNArgs(1),
	RunE: runServicesSync,
}

func init() {
	servicesSyncCmd.Flags().String("cid", "", "cluster ID (optional; defaults to the yaml's cid: field, cross-checked against the yaml when set)")
	servicesSyncCmd.Flags().Bool("dry-run", false, "compute the plan but don't apply anything")
	servicesSyncCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	servicesSyncCmd.Flags().Bool("redact-secrets", false, "redact field values in the plan output (used by the MCP wrapper)")
	servicesSyncCmd.Flags().BoolP("follow", "f", false, "wait for the emitted job to reach a terminal status; without it, prints the job ID and exits 0 the moment the conductor accepts the request")
	// --json mirrors apps_sync's contract: plan-only, valid with
	// --dry-run, emits the SyncPlan as JSON so CI gates can parse the
	// plan structurally. Regression target: I10-I (CLI parity gap).
	servicesSyncCmd.Flags().BoolP("json", "j", false, "emit the plan as JSON (requires --dry-run)")
}

func runServicesSync(cmd *cobra.Command, args []string) (rerr error) {
	cmd.SilenceUsage = true
	jsonOut, _ := cmd.Flags().GetBool("json")
	// planEmitted flips true once we've already written the plan JSON
	// to stdout. The refused-fields exit-1 path (I10-D) sets this so
	// the defer below doesn't emit a second JSON envelope after the
	// plan itself — stdout stays a single parseable JSON document.
	var planEmitted bool
	// Route any returned error through the JSON envelope when --json is
	// set so the failure path matches the success path's shape (mirrors
	// apps_sync's I4-G defer). Regression target: I10-I.
	defer func() {
		if jsonOut && rerr != nil && !planEmitted {
			rerr = emitJSONError(cmd, rerr)
		} else if jsonOut && rerr != nil && planEmitted {
			// Plan already on stdout; silence cobra's "Error: ..." print
			// so the exit code is the only signal, leaving stdout pure.
			cmd.SilenceErrors = true
		}
	}()
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	skipPrompt, _ := cmd.Flags().GetBool("yes")
	redact, _ := cmd.Flags().GetBool("redact-secrets")
	follow, _ := cmd.Flags().GetBool("follow")

	// --json is plan-only: it doesn't make sense alongside an apply step.
	// Mirrors apps_sync's same constraint (I10-I parity).
	if jsonOut && !dryRun {
		return fmt.Errorf("--json is only valid with --dry-run")
	}

	ctx, err := prepareServicesCmd(cmd)
	if err != nil {
		return err
	}

	if len(args) != 1 {
		return fmt.Errorf("pass the path to a runos.service.<cid>.<sid>.yaml file")
	}
	yamlPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve yaml path: %w", err)
	}

	local, err := services.Load(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("yaml file %q not found", yamlPath)
		}
		return fmt.Errorf("read yaml: %w", err)
	}
	if local.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", local.AID, ctx.cfg.AccountID)
	}
	if err := ctx.bindToYAML(local.CID); err != nil {
		return err
	}
	if !services.IsSupportedType(ctx.manifest, local.Type) {
		return fmt.Errorf("service type %q is not supported by the current manifest (run 'runos manifest update'?). Known types: %v", local.Type, services.ListSupportedTypes(ctx.manifest))
	}

	addCmd, err := services.AddCommand(ctx.manifest, local.Type)
	if err != nil {
		return err
	}
	updateCmd, err := services.UpdateCommand(ctx.manifest, local.Type)
	if err != nil {
		return err
	}
	// showCmd is consulted by refusedDrift to split "unknown field, typo?"
	// from "known but read-only / immutable after create" (I9-G). Best
	// effort: a missing show command is non-fatal; the refusal message
	// just falls back to the generic immutable wording.
	showCmd, _ := services.ShowCommand(ctx.manifest, local.Type)

	// Server state: only meaningful when local already has an id. Skipped
	// for create flows so we don't 404 on a fresh service.
	var server *services.ServiceYAML
	if local.ID != "" {
		server, err = services.Pull(ctx.exec, ctx.manifest, local.Type, ctx.cid, ctx.cfg.AccountID, local.ID)
		if err != nil {
			return fmt.Errorf("fetch server state: %w", err)
		}
	}

	plan := services.ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)

	// JSON path: emit the plan as a single JSON object so CI gates can
	// parse the plan structurally. Mirrors apps_sync. Honour redact at
	// this layer too. Regression targets: I10-I (parity) + I10-M-style
	// secrets-in-json safety.
	if jsonOut {
		if redact {
			plan.RedactSecrets()
		}
		if err := emitJSON(plan); err != nil {
			return err
		}
		planEmitted = true
		// Refused fields are a CI-actionable signal: a typo'd field name
		// or an immutable-after-create edit needs human attention. Exit
		// non-zero in JSON mode so the CI gate trips. The defer above
		// skips the JSON-envelope wrap because planEmitted is true;
		// stdout stays a single parseable plan JSON. Regression target:
		// I10-D.
		if len(plan.Refused) > 0 {
			return fmt.Errorf("plan refused %d field(s); inspect the JSON for details", len(plan.Refused))
		}
		return nil
	}

	printServicesSyncPlan(plan, redact)

	if !plan.HasChanges() {
		fmt.Println("\nNothing to sync.")
		if len(plan.Refused) > 0 {
			return fmt.Errorf("plan refused %d field(s); see the refused section above", len(plan.Refused))
		}
		return nil
	}
	if dryRun {
		fmt.Println("\nDry run, no changes applied.")
		if len(plan.Refused) > 0 {
			// Same exit-1 trip as the JSON path so dry-run + text mode
			// also surfaces refused fields via the process exit code
			// (I10-D). The text plan already printed the refused list.
			return fmt.Errorf("plan refused %d field(s); see the refused section above", len(plan.Refused))
		}
		return nil
	}
	// Confirm before applying. Three short-circuits, matching the
	// `runos deploy` pattern (I14-D):
	//   1. `--yes` / `-y` set explicitly: skip the prompt verbatim.
	//   2. stdin is not a terminal (CI / piped invocation): treat as
	//      explicit skip. The prompt is useless when no human can see
	//      it, and `confirm` reading EOF from a closed pipe returned
	//      `read confirmation: EOF`, breaking IaC pipelines using
	//      services sync for drift reconciliation. The user has
	//      already authored the change in the yaml on disk; running
	//      sync against it implies intent.
	//   3. Otherwise prompt as before.
	if !skipPrompt && term.IsTerminal(int(os.Stdin.Fd())) {
		ok, err := confirm(fmt.Sprintf("\nApply changes to %s/%s on cluster %s? [y/N] ", plan.Type, plan.ID, plan.CID))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Aborted.")
			return nil
		}
	}

	res, err := services.ApplySyncPlan(ctx.exec, plan, addCmd, updateCmd)
	if err != nil {
		return err
	}

	// On create, persist the new id back to the yaml so subsequent
	// runs use the PATCH path. Strip nothing else — server-applied
	// defaults (e.g. resourceRequirementClassId) still need a follow-up
	// pull, same async-write race as apps_sync.
	if res.NewID != "" && local.ID == "" {
		local.ID = res.NewID
		if err := services.Save(yamlPath, local); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: provisioned service %s but failed to write id back to yaml: %v\n", res.NewID, err)
		} else {
			fmt.Printf("\nProvisioned %s/%s (id written to %s)\n", plan.Type, res.NewID, yamlPath)
		}
	}

	fmt.Println("\nSync complete.")
	if res.JobID == "" {
		return nil
	}
	if !follow {
		fmt.Printf("  job: %s\n", res.JobID)
		fmt.Printf("Follow rollout: runos follow %s\n", res.JobID)
		return nil
	}
	// --follow: stream progress until terminal. Surfaces the conductor's
	// job error verbatim on `failed` and exits non-zero. Keeps the
	// project-wide convention (CLAUDE.md "Job-following convention"):
	// follow is opt-in; without it, services_sync stays fire-and-forget.
	jobsSvc, err := jobs.NewService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to attach to job-progress stream: %v\n", err)
		fmt.Printf("  job: %s\n", res.JobID)
		fmt.Printf("Follow rollout: runos follow %s\n", res.JobID)
		return err
	}
	fmt.Printf("Following job %s...\n", res.JobID)
	jobCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := jobs.FollowJobWithService(jobCtx, jobsSvc, res.JobID); err != nil {
		return fmt.Errorf("job failed: %w", err)
	}
	return nil
}

// printServicesSyncPlan renders the plan in apps_sync's section-rule
// style: a header, the diff body, and any refused fields. redact swaps
// values for "<redacted>" markers when the plan output flows into LLM
// context via the MCP wrapper.
func printServicesSyncPlan(plan *services.SyncPlan, redact bool) {
	// Title shape: include the id when known. CREATE plans don't have one
	// yet, so render "<new>" as a placeholder rather than leaving the slot
	// blank ("Sync plan for minio/ on cluster mycluster2" was confusing).
	idSlot := plan.ID
	if idSlot == "" {
		idSlot = "<new>"
	}
	fmt.Printf("Sync plan for %s/%s on cluster %s\n", plan.Type, idSlot, plan.CID)

	if plan.CreateBody != nil {
		fmt.Println()
		fmt.Println(sectionRule("create", "POST"))
		printBody(plan.CreateBody, redact, "  ")
		// CREATE flow: no server state, so the hint relies on the body
		// carrying both the named RRC and an override directly.
		if hint := services.CustomSynthesisHint(plan.CreateBody, ""); hint != "" {
			fmt.Println()
			fmt.Printf("  %s\n", hint)
		}
	}
	if plan.PatchBody != nil {
		fmt.Println()
		fmt.Println(sectionRule("yaml", "patch"))
		if plan.Diff != "" {
			printIndented(plan.Diff, "  ")
		} else {
			printBody(plan.PatchBody, redact, "  ")
		}
		// UPDATE flow: even a PATCH that only carries an override field
		// (no RRC in body) flips RRC server-side when the stored class
		// is named. Pass the server-stored RRC so the hint catches the
		// "I just changed replicas, why did my class flip?" footgun.
		if hint := services.CustomSynthesisHint(plan.PatchBody, plan.ServerRRC); hint != "" {
			fmt.Println()
			fmt.Printf("  %s\n", hint)
		}
	}

	if len(plan.Refused) > 0 {
		fmt.Println()
		fmt.Println(sectionRule("refused", "cannot push"))
		for _, r := range plan.Refused {
			fmt.Printf("  %s\n", r)
		}
	}
}

// printBody renders a body map as indented `key: value` lines. Stable
// output order matters for diff parity against the unified diff form.
//
// redact is scoped to keys that look sensitive (password, token, secret,
// apiKey, credentials, ...). Service config fields like `name`,
// `resourceRequirementClassId`, `storageMb`, `replicas` are non-secret
// by design and stay legible so the user can confirm a CREATE plan.
// Pre-fix, redact wiped every value indiscriminately, leaving the user
// confirming a `name: <redacted>` create with no idea what they were
// about to provision.
func printBody(body map[string]any, redact bool, indent string) {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := fmt.Sprintf("%v", body[k])
		if redact && isSensitiveKey(k) {
			val = "<redacted>"
		}
		fmt.Printf("%s%s: %s\n", indent, k, val)
	}
}

// isSensitiveKey reports whether a service-config field name conventionally
// holds sensitive data. Used by the redact gate so non-secret config
// (display name, resource class enum, capacity, replicas) stays visible
// in plan output.
func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, marker := range []string{"password", "secret", "token", "apikey", "api_key", "credential", "privatekey", "private_key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
