package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/apps"

	"github.com/spf13/cobra"
)

var appsSyncCmd = &cobra.Command{
	Use:   "sync [yaml-file]",
	Short: "Push local app config to the cluster",
	Long: `Sync the local yaml + env + secret-files + overrides for a single app
back to the cluster. The reverse of "apps pull".

If no yaml file is passed, sync scans the current directory for a unique
runos*.yaml that parses as a pulled-app manifest (so "cd into the per-app
dir, run runos apps sync" works with no arguments). Zero or multiple
matches produce a directly-actionable error.

The yaml file is the manifest. Sync reads it, follows the file references
inside (env, secretFiles[].local, overrides[].local), and pushes the lot
to the cluster identified by the yaml's own cid + aid + id fields.

The cluster id comes from the yaml's cid: field by default. Pass --cid
(or set a default via "runos clusters default") to assert a specific
cluster: the value is cross-checked against the yaml and any mismatch
refuses the push, so you can't accidentally target the wrong cluster.

Sync runs in two phases:
  1. Plan: fetch current server state, compare against your local files,
           and print what would change. Nothing is written.
  2. Apply: after you confirm, push the changes via the appropriate API
           endpoints.

What gets pushed (for fields the server allows updating):
  - yaml (PATCH /apps/:id)             : replicas, ports, healthCheck, etc.
  - env  (POST /apps/:id/env-vars)     : replace-all
  - secret files (POST /apps/:id/secret-files): add + remove deltas
  - overrides (per-override CRUD)

What sync cannot push today (server has no endpoint):
  - VCS / integration fields (deployType, repo, branch). Use the console.

Examples:
  cd runos.mycluster3.appid4 && runos apps sync                   # auto-detect
  runos apps sync runos.mycluster3.appid4/runos.yaml              # explicit path
  runos apps sync <yaml> --dry-run                         # plan only
  runos apps sync <yaml> --yes                             # skip the confirm`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAppsSync,
}

func init() {
	appsSyncCmd.Flags().String("cid", "", "cluster ID (optional; defaults to the yaml's cid: field, cross-checked against the yaml when set)")
	appsSyncCmd.Flags().Bool("dry-run", false, "compute the plan but don't apply anything")
	appsSyncCmd.Flags().BoolP("yes", "y", false, "skip the confirmation prompt")
	appsSyncCmd.Flags().Bool("redact-secrets", false, "replace env values in the plan output with <redacted> markers (used by the MCP wrapper to keep secrets out of LLM context)")
}

func runAppsSync(cmd *cobra.Command, args []string) error {
	ctx, err := prepareAppsCmd(cmd)
	if err != nil {
		return err
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	skipPrompt, _ := cmd.Flags().GetBool("yes")

	yamlPath, err := resolveYamlArg(args, "sync")
	if err != nil {
		return err
	}
	yamlDir := filepath.Dir(yamlPath)

	localApp, err := apps.LoadLocalApp(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("yaml file %q not found", yamlPath)
		}
		return fmt.Errorf("read yaml: %w", err)
	}
	if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
		return fmt.Errorf("yaml at %q is missing id/cid/aid, pull again to refresh", yamlPath)
	}
	if localApp.AID != ctx.cfg.AccountID {
		return fmt.Errorf("yaml is for account %q but you're logged in as %q", localApp.AID, ctx.cfg.AccountID)
	}
	if err := ctx.bindToYAML(localApp.CID); err != nil {
		return err
	}

	// Sync no longer provisions services. Any requires entry without an
	// id is a yaml that hasn't been linked to a real service yet, point
	// the user to services_sync (which is the only place new services
	// are created in the IaC flow). The class:-shorthand creation path
	// is reserved for runos deploy, which writes a service yaml as a
	// side effect.
	for alias, r := range localApp.Requires {
		if r.ID != "" {
			continue
		}
		return fmt.Errorf(
			"requires.%s has no id: apps_sync does not provision services. "+
				"Create a runos.service.%s.<sid>.yaml (e.g. via 'runos services pull --type %s --service-id <existing-sid>' "+
				"or hand-author a yaml without id and run 'runos services sync' to create), "+
				"then set requires.%s.id to the service's id and re-run apps_sync.",
			alias, ctx.cid, r.Type, alias,
		)
	}

	appID := localApp.ID
	svc := ctx.svc

	// Load both env-var sources independently. Each maps to its own K8s
	// resource (Secret vs ConfigMap) on the cluster so syncs are diffed
	// and applied per-side.
	localSecretEnv, _, err := apps.LoadLocalEnv(yamlDir, localApp.SecretEnv)
	if err != nil {
		return fmt.Errorf("read local secret env file: %w", err)
	}
	localEnv, _, err := apps.LoadLocalEnv(yamlDir, localApp.Env)
	if err != nil {
		return fmt.Errorf("read local env file: %w", err)
	}

	// Pre-flight conflict check: a key in both files is a deterministic
	// failure on the conductor side, refuse the sync up-front.
	if conflicts := envKeyConflicts(localSecretEnv, localEnv); len(conflicts) > 0 {
		return fmt.Errorf(
			"env-var keys appear in both %s and %s: %s. "+
				"Move each key to exactly one file before syncing.",
			localApp.SecretEnv, localApp.Env, strings.Join(conflicts, ", "),
		)
	}

	// Soft-warn about local secret-env keys claimed by requires.<alias>.env.
	// Conductor drops these from customSecretEnvVars at sync time, so the
	// push is harmless and we don't refuse it. The cleanup is purely
	// cosmetic: the local file stays in sync with the file's apparent truth.
	// Only applies to the secret side because requires-derived vars only
	// land in the Secret.
	syncRequiresEnvCollisions := apps.FindServerInjectedEnvCollisions(localSecretEnv, localApp.Requires)
	if len(syncRequiresEnvCollisions) > 0 {
		fmt.Fprint(os.Stderr, "Note: ")
		fmt.Fprint(os.Stderr, apps.FormatServerInjectedEnvCollisions(syncRequiresEnvCollisions, localApp.SecretEnv))
		fmt.Fprintln(os.Stderr)
	}

	localSecrets, err := apps.LoadLocalSecretFiles(yamlDir, localApp.SecretFiles)
	if err != nil {
		return fmt.Errorf("read local secret files: %w", err)
	}
	localOverrides, err := apps.LoadLocalOverrides(yamlDir, localApp.Overrides)
	if err != nil {
		return fmt.Errorf("read local overrides: %w", err)
	}

	// Fetch current server state.
	raw, err := svc.GetApp(appID)
	if err != nil {
		return fmt.Errorf("fetch server app: %w", err)
	}
	serverSecretEnv, err := svc.GetAppSecretEnvVars(appID)
	if err != nil {
		return fmt.Errorf("fetch server secret env: %w", err)
	}
	serverEnv, err := svc.GetAppEnvVars(appID)
	if err != nil {
		return fmt.Errorf("fetch server env: %w", err)
	}
	serverSecrets, err := svc.ListSecretFiles(appID)
	if err != nil {
		return fmt.Errorf("list server secret files: %w", err)
	}
	serverOverrides, err := svc.ListOverrides(appID)
	if err != nil {
		return fmt.Errorf("list server overrides: %w", err)
	}
	serverRequires, err := svc.GetAppRequires(appID)
	if err != nil {
		return fmt.Errorf("read server requires: %w", err)
	}

	plan := apps.ComputeSyncPlan(apps.SyncInputs{
		LocalApp:            localApp,
		LocalSecretEnvVars:  localSecretEnv,
		LocalEnvVars:        localEnv,
		LocalSecretFiles:    localSecrets,
		LocalOverrides:      localOverrides,
		ServerRaw:           raw,
		ServerSecretEnvVars: serverSecretEnv,
		ServerEnvVars:       serverEnv,
		ServerSecretFiles:   serverSecrets,
		ServerOverrides:     serverOverrides,
		ServerRequires:      serverRequires,
	})

	redactSecrets, _ := cmd.Flags().GetBool("redact-secrets")
	printSyncPlan(plan, redactSecrets)

	if !plan.HasChanges() {
		fmt.Println("\nNothing to sync.")
		return nil
	}
	if dryRun {
		fmt.Println("\nDry run, no changes applied.")
		return nil
	}
	if !skipPrompt {
		ok, err := confirm(fmt.Sprintf("\nApply changes to %s (%s) on cluster %s? [y/N] ", plan.AppName, plan.AppID, plan.CID))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Apply the plan and stop. We deliberately do NOT re-pull from the
	// server afterwards: the PATCH triggers an async job and a GET
	// issued here would race the Firestore write. The local yaml stays
	// the source of truth; the user runs apps_pull when they want the
	// server-applied normalisation on disk.
	return applySyncPlan(svc, plan)
}

// printSyncPlan renders the plan in the same visual style as diff: section
// rules with status, tight per-section bodies, hidden when nothing to do.
//
// redactEnv replaces env values with "<redacted>" markers (used by the
// MCP wrapper so env values never flow into LLM context).
func printSyncPlan(plan *apps.SyncPlan, redactEnv bool) {
	fmt.Printf("Sync plan for %s (%s) on cluster %s\n", plan.AppName, plan.AppID, plan.CID)

	for _, note := range plan.Notes {
		fmt.Println()
		fmt.Printf("Note: %s\n", note)
	}

	var unchanged []string

	if len(plan.YAMLPatch) > 0 {
		fmt.Println()
		fmt.Println(sectionRule("yaml", "patch"))
		printIndented(plan.YAMLDiff, "  ")
	} else {
		unchanged = append(unchanged, "yaml")
	}

	if plan.SecretEnv != nil && plan.SecretEnv.HasChanges() {
		fmt.Println()
		fmt.Println(sectionRule("secretEnv", "replace-all"))
		printEnvChange(plan.SecretEnv, redactEnv)
	} else {
		unchanged = append(unchanged, "secretEnv")
	}

	if plan.Env != nil && plan.Env.HasChanges() {
		fmt.Println()
		fmt.Println(sectionRule("env", "replace-all"))
		// Plain env values are non-sensitive by definition (they're committed
		// to VCS); show them verbatim regardless of the redact flag.
		printEnvChange(plan.Env, false)
	} else {
		unchanged = append(unchanged, "env")
	}

	if plan.SecretFiles != nil && plan.SecretFiles.HasChanges() {
		fmt.Println()
		fmt.Println(sectionRule("secretFiles", "delta"))
		printSecretFilesChange(plan.SecretFiles)
	} else {
		unchanged = append(unchanged, "secretFiles")
	}

	if len(plan.Overrides) > 0 {
		fmt.Println()
		fmt.Println(sectionRule("overrides", "delta"))
		printOverrideOps(plan.Overrides)
	} else {
		unchanged = append(unchanged, "overrides")
	}

	if len(plan.RefusedYAML) > 0 {
		fmt.Println()
		fmt.Println(sectionRule("refused", "cannot push"))
		for _, r := range plan.RefusedYAML {
			fmt.Printf("  %s\n", r)
		}
		fmt.Println("  These fields have no server endpoint yet, change them via the console.")
	}

	if len(unchanged) > 0 {
		fmt.Println()
		fmt.Printf("Unchanged: %s\n", strings.Join(unchanged, ", "))
	}
}

func printEnvChange(e *apps.EnvChange, redact bool) {
	keys := sortedKeys(e.Add)
	for _, k := range keys {
		if redact {
			fmt.Printf("  + %s=<redacted>\n", k)
		} else {
			fmt.Printf("  + %s=%s\n", k, e.Add[k])
		}
	}
	keys = sortedKeys(e.Update)
	for _, k := range keys {
		if redact {
			fmt.Printf("  ~ %s=<redacted>\n", k)
		} else {
			fmt.Printf("  ~ %s=%s\n", k, e.Update[k])
		}
	}
	for _, k := range e.Remove {
		fmt.Printf("  - %s   (server has it; replace-all will delete)\n", k)
	}
	// Platform-injected names: server has them, local doesn't, but the local
	// yaml's requires.<alias>.env claims them. Conductor's
	// app.updateSecretEnvVars orchestration re-derives these on every push
	// and they always win on conflict, so they're NOT going to be deleted.
	// Render them under a separate marker so the user (and any LLM driving
	// the sync) doesn't read the line as a destructive op.
	for _, k := range e.PreservedByPlatform {
		fmt.Printf("  = %s   (platform-injected via requires.<alias>.env; preserved on push)\n", k)
	}
}

func printSecretFilesChange(s *apps.SecretFilesChange) {
	for _, f := range s.Add {
		fmt.Printf("  + %s   (mount %s)\n", f.Filename, f.MountPath)
	}
	for _, f := range s.Update {
		fmt.Printf("  ~ %s\n", f.Filename)
	}
	for _, name := range s.Remove {
		fmt.Printf("  - %s   (server has it; local doesn't)\n", name)
	}
}

func printOverrideOps(ops []apps.OverrideOp) {
	for _, o := range ops {
		switch o.Op {
		case "add":
			suffix := ""
			if o.Reason != "" {
				suffix = "   (" + o.Reason + ")"
			}
			fmt.Printf("  + %s%s\n", o.Name, suffix)
		case "update":
			fmt.Printf("  ~ %s (%s)\n", o.Name, o.ID)
			if o.UnifiedDiff != "" {
				fmt.Println()
				printIndented(trimTrailingBlankLines(o.UnifiedDiff), "    ")
				fmt.Println()
			}
		case "delete":
			fmt.Printf("  - %s (%s)   (server has it; local doesn't)\n", o.Name, o.ID)
		}
	}
}

// applySyncPlan executes the plan. yaml goes first (PATCH so subsequent
// redeploys see the right config). env + secret-files together when both
// change (atomic /secrets endpoint), otherwise the dedicated endpoints.
// Overrides last, one CRUD call each. Per-step failures abort the rest.
func applySyncPlan(svc *apps.Service, plan *apps.SyncPlan) error {
	fmt.Println()
	fmt.Println("Applying...")

	if len(plan.YAMLPatch) > 0 {
		jobID, err := svc.UpdateApp(plan.AppID, plan.YAMLPatch)
		if err != nil {
			return fmt.Errorf("yaml patch: %w", err)
		}
		fmt.Printf("  yaml: PATCH ok%s\n", jobIDSuffix(jobID))
	}

	secretEnvChange := plan.SecretEnv != nil && plan.SecretEnv.HasChanges()
	envChange := plan.Env != nil && plan.Env.HasChanges()
	secChange := plan.SecretFiles != nil && plan.SecretFiles.HasChanges()

	// Bundle into the atomic /secrets endpoint when more than one source
	// changes, so the app only redeploys once. The narrow endpoints stay
	// for the single-source cases where they're cheaper for the conductor
	// (no template reapply on env-only changes).
	switch {
	case (secretEnvChange && envChange) || (secretEnvChange && secChange) || (envChange && secChange):
		var secretFinal, envFinal map[string]string
		if secretEnvChange {
			secretFinal = plan.SecretEnv.Final
		}
		if envChange {
			envFinal = plan.Env.Final
		}
		var add []apps.SecretFilePayload
		var remove []string
		if secChange {
			add = plan.SecretFiles.AllAddPayloads()
			remove = plan.SecretFiles.Remove
		}
		jobID, err := svc.UpdateSecrets(plan.AppID, secretFinal, envFinal, add, remove)
		if err != nil {
			return fmt.Errorf("secret env + env + secret files: %w", err)
		}
		fmt.Printf("  secretEnv/env/secretFiles: atomic update ok%s\n", jobIDSuffix(jobID))
	case secretEnvChange:
		jobID, err := svc.ReplaceSecretEnvVars(plan.AppID, plan.SecretEnv.Final)
		if err != nil {
			return fmt.Errorf("secret env: %w", err)
		}
		fmt.Printf("  secretEnv: replace ok%s\n", jobIDSuffix(jobID))
	case envChange:
		jobID, err := svc.ReplaceEnvVars(plan.AppID, plan.Env.Final)
		if err != nil {
			return fmt.Errorf("env: %w", err)
		}
		fmt.Printf("  env: replace ok%s\n", jobIDSuffix(jobID))
	case secChange:
		add := plan.SecretFiles.AllAddPayloads()
		jobID, err := svc.UpdateSecretFiles(plan.AppID, add, plan.SecretFiles.Remove)
		if err != nil {
			return fmt.Errorf("secret files: %w", err)
		}
		fmt.Printf("  secretFiles: update ok%s\n", jobIDSuffix(jobID))
	}

	for _, o := range plan.Overrides {
		switch o.Op {
		case "add":
			id, jobID, err := svc.AddOverride(plan.AppID, o.Name, o.Content, o.Enabled)
			if err != nil {
				return fmt.Errorf("override %q add: %w", o.Name, err)
			}
			fmt.Printf("  override %q: created (id %s)%s\n", o.Name, id, jobIDSuffix(jobID))
		case "update":
			name := o.Name
			enabled := o.Enabled
			jobID, err := svc.UpdateOverride(plan.AppID, o.ID, &name, o.Content, &enabled)
			if err != nil {
				return fmt.Errorf("override %q update: %w", o.Name, err)
			}
			fmt.Printf("  override %q: updated%s\n", o.Name, jobIDSuffix(jobID))
		case "delete":
			jobID, err := svc.DeleteOverride(plan.AppID, o.ID)
			if err != nil {
				return fmt.Errorf("override %q delete: %w", o.Name, err)
			}
			fmt.Printf("  override %q: deleted%s\n", o.Name, jobIDSuffix(jobID))
		}
	}

	fmt.Println("\nSync complete.")
	return nil
}

func jobIDSuffix(jobID string) string {
	if jobID == "" {
		return ""
	}
	return "  (job " + jobID + ")"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// confirm prompts on stderr and reads a single line from stdin. Treats
// "y" / "yes" (case-insensitive) as true; everything else as false.
func confirm(prompt string) (bool, error) {
	fmt.Fprint(os.Stderr, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}
