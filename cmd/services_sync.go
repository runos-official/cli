package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
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
	servicesSyncCmd.Flags().String("cid", "", "cluster ID (overrides default; cross-checked against the yaml)")
	servicesSyncCmd.Flags().Bool("dry-run", false, "compute the plan but don't apply anything")
	servicesSyncCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	servicesSyncCmd.Flags().Bool("redact-secrets", false, "redact field values in the plan output (used by the MCP wrapper)")
}

func runServicesSync(cmd *cobra.Command, args []string) error {
	ctx, err := prepareServicesCmd(cmd)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	skipPrompt, _ := cmd.Flags().GetBool("yes")
	redact, _ := cmd.Flags().GetBool("redact-secrets")

	if len(args) != 1 {
		return fmt.Errorf("pass the path to a runos.service.<cid>.<sid>.yaml file")
	}
	yamlPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve yaml path: %w", err)
	}

	local, err := services.Load(yamlPath)
	if err != nil {
		return fmt.Errorf("read yaml: %w", err)
	}
	if local.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", local.AID, ctx.cfg.AccountID)
	}
	if local.CID != ctx.cid {
		return fmt.Errorf("cluster mismatch: yaml is for %q but --cid (or default) is %q", local.CID, ctx.cid)
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

	// Server state: only meaningful when local already has an id. Skipped
	// for create flows so we don't 404 on a fresh service.
	var server *services.ServiceYAML
	if local.ID != "" {
		server, err = services.Pull(ctx.exec, ctx.manifest, local.Type, ctx.cid, ctx.cfg.AccountID, local.ID)
		if err != nil {
			return fmt.Errorf("fetch server state: %w", err)
		}
	}

	plan := services.ComputeSyncPlan(local, server, addCmd, updateCmd)
	printServicesSyncPlan(plan, redact)

	if !plan.HasChanges() {
		fmt.Println("\nNothing to sync.")
		return nil
	}
	if dryRun {
		fmt.Println("\nDry run, no changes applied.")
		return nil
	}
	if !skipPrompt {
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
	if res.JobID != "" {
		fmt.Printf("  job: %s\n", res.JobID)
	}
	return nil
}

// printServicesSyncPlan renders the plan in apps_sync's section-rule
// style: a header, the diff body, and any refused fields. redact swaps
// values for "<redacted>" markers when the plan output flows into LLM
// context via the MCP wrapper.
func printServicesSyncPlan(plan *services.SyncPlan, redact bool) {
	fmt.Printf("Sync plan for %s/%s on cluster %s\n", plan.Type, plan.ID, plan.CID)

	if plan.CreateBody != nil {
		fmt.Println()
		fmt.Println(sectionRule("create", "POST"))
		printBody(plan.CreateBody, redact, "  ")
	}
	if plan.PatchBody != nil {
		fmt.Println()
		fmt.Println(sectionRule("yaml", "patch"))
		if plan.Diff != "" {
			printIndented(plan.Diff, "  ")
		} else {
			printBody(plan.PatchBody, redact, "  ")
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
func printBody(body map[string]any, redact bool, indent string) {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := fmt.Sprintf("%v", body[k])
		if redact {
			val = "<redacted>"
		}
		fmt.Printf("%s%s: %s\n", indent, k, val)
	}
}
