package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/jobs"

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
	appsSyncCmd.Flags().Bool("allow-empty-secret-env", false, "allow apps_sync to wipe ALL server-side secret env vars when the local secret-env file is empty or missing (otherwise refused as a footgun)")
	appsSyncCmd.Flags().Bool("redact-secrets", false, "replace env values in the plan output with <redacted> markers (used by the MCP wrapper to keep secrets out of LLM context)")
	appsSyncCmd.Flags().BoolP("follow", "f", false, "wait for each emitted job to reach a terminal status before printing 'ok'; without it, prints the job ID and continues (failures land silently on the cluster)")
}

func runAppsSync(cmd *cobra.Command, args []string) error {
	ctx, err := prepareAppsCmd(cmd)
	if err != nil {
		return err
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	skipPrompt, _ := cmd.Flags().GetBool("yes")
	allowEmptySecretEnv, _ := cmd.Flags().GetBool("allow-empty-secret-env")
	follow, _ := cmd.Flags().GetBool("follow")

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
	// and applied per-side. When the yaml omits the ref, fall back to the
	// documented default path (V3 fix): apps_pull writes there by default,
	// so sync MUST also read there or a fresh checkout silently skips
	// pushing the file's contents.
	localSecretEnv, _, err := apps.LoadLocalEnv(yamlDir, localApp.SecretEnv, apps.SecretEnvFilename(localApp.CID, localApp.ID))
	if err != nil {
		return fmt.Errorf("read local secret env file: %w", err)
	}
	localEnv, _, err := apps.LoadLocalEnv(yamlDir, localApp.Env, apps.EnvFilename(localApp.CID, localApp.ID))
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

	// (No platform-claimed-keys note here — those keys are by design
	// always present in the local secret env file, written there by
	// apps_pull and re-merged on every deploy. The previous "remove these,
	// they're dead config" advice was wrong-headed: removing them makes
	// local LESS accurate, not more, since the next pull writes them back.
	// The plain-side equivalent — platform-claimed credentials in the
	// VCS-committed config.env — is a real footgun but is hard-refused
	// server-side via the secret/plain conflict gate, so we don't need a
	// client-side check for it either.)

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

	// Refuse to silently wipe server-side secret env vars when the
	// effective USER push is empty. "Effective" means: filter out any
	// key in Final that's claimed by the local yaml's requires.<alias>.env
	// mappings (those round-trip on every push and don't represent user
	// intent). Catches both byte-empty local files AND files that
	// contain only platform-injected names like DATABASE_URL.
	//
	// The destructive case must be opt-in via --allow-empty-secret-env.
	// `--yes` is for the confirmation prompt only; widening it to also
	// waive destructive ops would defeat the gate's purpose (a CI runner
	// with --yes shouldn't accidentally clear production secrets).
	platformInjectedLocal := map[string]bool{}
	for _, c := range apps.FindServerInjectedEnvCollisions(localSecretEnv, localApp.Requires) {
		platformInjectedLocal[c.EnvVar] = true
	}
	if err := apps.CheckEmptySecretEnvWipe(plan, allowEmptySecretEnv, platformInjectedLocal); err != nil {
		return err
	}
	if allowEmptySecretEnv && plan.SecretEnv != nil && len(plan.SecretEnv.Remove) > 0 {
		// Re-check the user-final-empty signal so we only warn when the
		// gate would have fired without the flag.
		userFinalEmpty := true
		for k := range plan.SecretEnv.Final {
			if !platformInjectedLocal[k] {
				userFinalEmpty = false
				break
			}
		}
		if userFinalEmpty {
			fmt.Fprintf(os.Stderr, "Warning: replacing %d server-side secret env key(s) with an effectively-empty local set (--allow-empty-secret-env).\n", len(plan.SecretEnv.Remove))
		}
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
	return applySyncPlan(svc, plan, yamlDir, follow)
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
//
// follow controls per-step blocking on the conductor-emitted jobID. Off by
// default (the project-wide convention; see CLAUDE.md "Job-following
// convention"): apps_sync prints `X: ok (job <id>)` per step and exits 0
// the moment the API accepts the request. With `--follow`, each step
// streams progress via jobs.FollowJobWithService and the dispatch aborts
// the rest of the plan on terminal `failed`, surfacing the conductor's
// `job.Error` verbatim and exiting non-zero.
//
// The opt-in shape matches `runos deploy --follow` exactly. Both verbs
// always stream when follow is on (no TTY split): CI users get the per-
// work-item lines for free, which is useful for debugging silent cluster-
// side failures. Pre-V12, every "ok" line printed regardless of the
// underlying job's outcome — the follow flag now lets users opt into
// observing the actual outcome.
func applySyncPlan(svc *apps.Service, plan *apps.SyncPlan, yamlDir string, follow bool) error {
	fmt.Println()
	fmt.Println("Applying...")

	// Build a jobs.Service once (it loads config + auth) so per-step waits
	// share the same client. Constructed lazily because some plans don't
	// produce any jobIDs at all (e.g. all-overrides-CRUD-no-side-effects)
	// and we don't want to fail apply on an auth blip when there's no
	// follow work to do.
	var jobsSvc *jobs.Service
	getJobsSvc := func() (*jobs.Service, error) {
		if jobsSvc != nil {
			return jobsSvc, nil
		}
		s, err := jobs.NewService()
		if err != nil {
			return nil, err
		}
		jobsSvc = s
		return jobsSvc, nil
	}

	var queuedJobIDs []string
	track := func(jobID string) {
		if jobID != "" {
			queuedJobIDs = append(queuedJobIDs, jobID)
		}
	}

	// finishStep prints the per-step closing line. When follow is off (the
	// default), prints `X: ok (job <id>)` and continues. When follow is on
	// AND the step emitted a jobID, streams progress to stdout via the
	// shared FollowJobWithService and surfaces the conductor's job error
	// verbatim on terminal `failed`.
	finishStep := func(label, jobID string) error {
		if !follow || jobID == "" {
			fmt.Printf("  %s ok%s\n", label, jobIDSuffix(jobID))
			return nil
		}
		fmt.Printf("  %s queued (job %s)...\n", label, jobID)
		js, err := getJobsSvc()
		if err != nil {
			fmt.Printf("  %s FAILED to attach to job-progress stream: %v\n", label, err)
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if waitErr := jobs.FollowJobWithService(ctx, js, jobID); waitErr != nil {
			fmt.Printf("  %s FAILED: %v\n", label, waitErr)
			return waitErr
		}
		fmt.Printf("  %s ok\n", label)
		return nil
	}

	if len(plan.YAMLPatch) > 0 {
		jobID, err := svc.UpdateApp(plan.AppID, plan.YAMLPatch)
		if err != nil {
			return fmt.Errorf("yaml patch: %w", err)
		}
		track(jobID)
		if err := finishStep("yaml: PATCH", jobID); err != nil {
			return fmt.Errorf("yaml patch: %w", err)
		}
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
		track(jobID)
		if err := finishStep("secretEnv/env/secretFiles: atomic update", jobID); err != nil {
			return fmt.Errorf("secret env + env + secret files: %w", err)
		}
	case secretEnvChange:
		jobID, err := svc.ReplaceSecretEnvVars(plan.AppID, plan.SecretEnv.Final)
		if err != nil {
			return fmt.Errorf("secret env: %w", err)
		}
		track(jobID)
		if err := finishStep("secretEnv: replace", jobID); err != nil {
			return fmt.Errorf("secret env: %w", err)
		}
	case envChange:
		jobID, err := svc.ReplaceEnvVars(plan.AppID, plan.Env.Final)
		if err != nil {
			return fmt.Errorf("env: %w", err)
		}
		track(jobID)
		if err := finishStep("env: replace", jobID); err != nil {
			return fmt.Errorf("env: %w", err)
		}
	case secChange:
		add := plan.SecretFiles.AllAddPayloads()
		jobID, err := svc.UpdateSecretFiles(plan.AppID, add, plan.SecretFiles.Remove)
		if err != nil {
			return fmt.Errorf("secret files: %w", err)
		}
		track(jobID)
		if err := finishStep("secretFiles: update", jobID); err != nil {
			return fmt.Errorf("secret files: %w", err)
		}
	}

	for _, o := range plan.Overrides {
		switch o.Op {
		case "add":
			id, jobID, err := svc.AddOverride(plan.AppID, o.Name, o.Content, o.Enabled)
			if err != nil {
				return fmt.Errorf("override %q add: %w", o.Name, err)
			}
			track(jobID)
			if err := finishStep(fmt.Sprintf("override %q: created (id %s)", o.Name, id), jobID); err != nil {
				return fmt.Errorf("override %q add: %w", o.Name, err)
			}
		case "update":
			name := o.Name
			enabled := o.Enabled
			jobID, err := svc.UpdateOverride(plan.AppID, o.ID, &name, o.Content, &enabled)
			if err != nil {
				return fmt.Errorf("override %q update: %w", o.Name, err)
			}
			track(jobID)
			if err := finishStep(fmt.Sprintf("override %q: updated", o.Name), jobID); err != nil {
				return fmt.Errorf("override %q update: %w", o.Name, err)
			}
		case "delete":
			jobID, err := svc.DeleteOverride(plan.AppID, o.ID)
			if err != nil {
				return fmt.Errorf("override %q delete: %w", o.Name, err)
			}
			track(jobID)
			// Best-effort cleanup of the local override file. Server-side
			// delete is the source of truth; a missing local file is
			// fine (manual prior delete), and an unlink failure shouldn't
			// fail the sync — leaves the orphan, same shape as before.
			if o.LocalLeaf != "" && yamlDir != "" {
				localPath := filepath.Join(yamlDir, apps.OverridesDirname(), o.LocalLeaf)
				if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(os.Stderr, "  warning: removed override %q server-side but couldn't unlink %s: %v\n", o.Name, localPath, err)
				}
			}
			if err := finishStep(fmt.Sprintf("override %q: deleted", o.Name), jobID); err != nil {
				return fmt.Errorf("override %q delete: %w", o.Name, err)
			}
		}
	}

	fmt.Println("\nSync complete.")

	// Rollout-confidence hint. The API calls succeeded, but the actual
	// K8s rollout (rolling restart, image patch, replica re-readiness) is
	// async on the cluster. Suppressed when follow is on (each job already
	// reached terminal status and was reported above) -- the hint is
	// redundant and just adds noise. Shown on --no-follow as before.
	if !follow {
		if len(queuedJobIDs) == 1 {
			fmt.Println()
			fmt.Printf("Follow rollout: runos follow %s\n", queuedJobIDs[0])
		} else if len(queuedJobIDs) > 1 {
			fmt.Println()
			fmt.Println("Follow rollout (each job is a separate orchestration):")
			for _, id := range queuedJobIDs {
				fmt.Printf("  runos follow %s\n", id)
			}
		}
	}
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
