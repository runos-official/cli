package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/config"
)

// preDeployDriftCheck refuses to deploy when the local yaml has diverged
// from the running app on the server. It only runs when configPath
// parses as a pulled-app yaml with id/cid/aid set; fresh deploy yamls
// (no ids yet) skip the gate silently. Errors fetching server state
// surface as warnings rather than hard failures so the gate doesn't
// block deploys when the API is briefly unavailable, the deploy itself
// will fail loudly anyway. Pass force=true to bypass the gate entirely.
//
// hasLegacy customises the refusal output: when the local yaml uses
// deprecated top-level fields (port:, domain:, standardHttps:), the
// drift is almost always a side effect of the schema mismatch rather
// than real local edits. We surface a tailored "migrate via apps pull"
// recommendation so the user (and any LLM driving the deploy) picks
// the migration path instead of `--force`-ing onto the legacy shape.
func preDeployDriftCheck(cfg *config.Config, token, cid, configPath string, force, hasLegacy, jsonOutput bool) error {
	// Issue 86: under --json, redirect os.Stdout to os.Stderr so the
	// drift refusal report (header + reconcile hints + printDiffReport)
	// doesn't pollute the JSON stdout contract. The JSON error envelope
	// still emits to stdout via runDeploy's defer'd emitJSONError.
	if jsonOutput {
		origStdout := os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = origStdout }()
	}
	localApp, err := apps.LoadLocalApp(configPath)
	if err != nil {
		// Yaml didn't parse as a pulled-app manifest. Two cases:
		//   1. Genuine bare deploy yaml that just doesn't carry the
		//      pulled-app shape (no id/cid/aid yet) — common, harmless.
		//   2. A pulled-app yaml with malformed content that happens to
		//      let DeployConfig.LoadConfig through but trips PulledApp.
		// We can't tell them apart from here. Surface a one-line note
		// so the user (and any LLM driving the deploy) sees that the
		// gate didn't run, then proceed — the deploy itself will fail
		// loudly if the yaml is genuinely broken.
		fmt.Fprintf(os.Stderr, "Note: pre-deploy drift gate skipped (yaml didn't parse as a pulled-app manifest: %v).\n", err)
		return nil
	}
	if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
		// Fresh deploy yaml: no upstream state to compare against.
		return nil
	}
	// Defence-in-depth: ids flow into URLs and (server-side) into
	// filesystem paths; reject anything outside the conductor identifier
	// alphabet so a tampered local yaml can't smuggle path components.
	if err := apps.ValidateIdentifier("app id", localApp.ID); err != nil {
		return fmt.Errorf("pre-deploy gate: %w", err)
	}
	if err := apps.ValidateIdentifier("cluster id", localApp.CID); err != nil {
		return fmt.Errorf("pre-deploy gate: %w", err)
	}

	appsSvc := apps.NewService(cfg.GetAPIURL(), token, cid, cfg.AccountID)
	report, err := apps.BuildDiffReport(appsSvc, localApp, configPath, cfg.AccountID, cid)
	if err != nil {
		// I14-B: a 404 from the drift gate's `GET /apps/:id` means the
		// app id in the local yaml no longer exists server-side (most
		// commonly: user deleted the app via console / `apps delete`
		// and then came back to a stale `runos.yaml`). Pre-fix the
		// gate emitted a generic "Warning: drift check failed" line
		// and proceeded with the deploy, which then failed at the
		// prepare-cli-deployment step with a different 400 — the user
		// hit a dead end with no recovery guidance. Surface the cause
		// + the two clean paths so a fresh-start deploy isn't a maze.
		var apiErr *apps.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			fmt.Fprintf(os.Stderr, "Error: app %q in %q no longer exists on cluster %q (server returned 404).\n", localApp.ID, configPath, cid)
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "Two recovery paths:")
			fmt.Fprintf(os.Stderr, "  1. Re-create as a fresh app: clear the `id:` line from %s and re-run `runos deploy`.\n", configPath)
			fmt.Fprintln(os.Stderr, "     The conductor mints a new app + osid; subsequent deploys pin to it.")
			fmt.Fprintf(os.Stderr, "  2. Bypass the gate: `runos deploy %s --force` (only useful if you genuinely want the\n", configPath)
			fmt.Fprintln(os.Stderr, "     prepare step to surface its own 404; doesn't re-create the app).")
			return fmt.Errorf("app deleted server-side; clear `id:` from yaml or pass --force")
		}
		fmt.Fprintf(os.Stderr, "Warning: pre-deploy drift check failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "Proceeding with deploy. Run 'runos apps diff %s' manually to verify.\n", configPath)
		return nil
	}
	// I4-B: deploy gate output flows into CI logs by default; redact
	// sensitive content (secret-env values, secret-file diffs) before
	// any printDiffReport call so credentials don't end up in build
	// pipelines. The user already authored these locally — the gate
	// is informational, not diagnostic. `apps diff` keeps its
	// `--redact-secrets` opt-in for users who explicitly want the
	// values rendered for inspection.
	report.RedactSecrets()
	// emitDeletionWarning surfaces server-only fields that a deploy
	// might clear. The warning is split into two buckets:
	//   - clearOnOmit: fields the server WILL wipe because they have
	//     omit-equals-clear semantics on the PATCH endpoint
	//     (apps.OmitClearFields).
	//   - preserveOnOmit: server-only fields that stay put on push.
	// Earlier versions of this warning hardcoded "healthCheck* and
	// metrics* fields will be CLEARED" regardless of which fields were
	// actually in scope, which mismatched the bulleted list whenever
	// only preserve-on-omit fields drifted (e.g. cpu*/memory*). We now
	// only print the clearing line when there's actually something to
	// clear, and we stay quiet entirely when nothing is server-only.
	emitDeletionWarning := func() {
		if len(report.YAML.ServerOnlyFields) == 0 {
			return
		}
		// I4-F: drop fields the deploy orchestration removes via a
		// step OTHER than the apps PATCH (currently `requires.*` —
		// `replaceDependencies` handles those edges, and as of
		// conductor R2 the orphan secret keys are stripped too). The
		// pre-fix message landed `requires.<alias> (3 fields)` under
		// "Preserved server-side (no action needed)" even though the
		// user had intentionally removed the alias and the post-
		// deploy state confirmed the removal. Filtering early keeps
		// the bulleted summary honest about what's actually still in
		// scope of the apps PATCH's omit-clear / omit-preserve rules.
		serverOnly := apps.FilterOrchestrationRemoved(report.YAML.ServerOnlyFields)
		if len(serverOnly) == 0 {
			return
		}
		clearOnOmit, preserveOnOmit := apps.PartitionServerOnlyByClearSemantics(serverOnly)
		if len(clearOnOmit) == 0 && len(preserveOnOmit) == 0 {
			return
		}
		fmt.Fprintln(os.Stderr, "Note: the server has fields your local yaml doesn't.")
		if len(clearOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  WILL be cleared by this deploy (omit-equals-clear):")
			for _, f := range clearOnOmit {
				fmt.Fprintf(os.Stderr, "    - %s\n", f)
			}
		}
		if len(preserveOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  Preserved server-side (no action needed):")
			for _, f := range preserveOnOmit {
				fmt.Fprintf(os.Stderr, "    - %s\n", f)
			}
		}
		if hint := apps.StandardHttpsResetHint(clearOnOmit); hint != "" {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, hint)
		}
		if len(clearOnOmit) > 0 {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "  To keep the cleared fields, cancel and run:")
			fmt.Fprintf(os.Stderr, "    runos apps pull %s --force\n", configPath)
			fmt.Fprintln(os.Stderr, "  which merges server state into your local yaml first.")
		}
		fmt.Fprintln(os.Stderr)
	}

	if !report.NeedsForceToDeploy() {
		// Gate isn't refusing. Under the new desired-state model that
		// covers two friendly cases:
		//   - No drift at all (deploy is a no-op for state).
		//   - Drift is purely "local has fields server doesn't" — the
		//     user added something locally, deploy will set it.
		// In the second case, server-only fields could still appear on
		// nested values, so we still emit the deletion warning when
		// applicable. Always print the unified plan when there's drift
		// so the user sees exactly what's about to change.
		if report.HasDrift() {
			fmt.Printf("\n%s (%s) on cluster %s, deploy plan:\n", report.AppName, report.AppID, report.CID)
			printDiffReport(report)
			fmt.Println()
			emitDeletionWarning()
		}
		return nil
	}

	if force {
		emitDeletionWarning()
		// Force path covers two different scenarios under the new
		// model: server-only fields (clears on push) and divergent
		// values (overwrite on push). In both cases the diff above
		// shows what's about to happen; we only need a one-liner
		// preface so the user (or LLM) knows --force is in effect.
		fmt.Fprintln(os.Stderr, deployDriftHeadlineForce(report))
		fmt.Fprintln(os.Stderr, "         Deploy will reconcile the server to match the local yaml.")
		if hasLegacy {
			fmt.Fprintln(os.Stderr, "         Note: this yaml uses top-level shorthand fields (port:/standardHttps:)")
			fmt.Fprintln(os.Stderr, "         that duplicate servicePortMappings[]. Forcing through means the same")
			fmt.Fprintln(os.Stderr, "         drift will reappear on every deploy.")
			fmt.Fprintf(os.Stderr, "         Recommended fix: runos apps pull %s --force\n", configPath)
		}
		fmt.Fprintln(os.Stderr)
		printDiffReport(report)
		fmt.Fprintln(os.Stderr)
		return nil
	}

	fmt.Printf("\n%s (%s) on cluster %s, %s\n", report.AppName, report.AppID, report.CID, deployDriftHeadline(report))
	fmt.Println("Deploying now would overwrite changes that aren't in your local files.")
	printDiffReport(report)
	fmt.Println()
	if hasLegacy {
		fmt.Println("Your runos.yaml uses top-level shorthand fields (`port:`, `standardHttps:`) that")
		fmt.Println("duplicate `servicePortMappings[]`. The server stores the canonical shape, which")
		fmt.Println("is the most likely cause of the drift above.")
		fmt.Println()
		fmt.Println("RECOMMENDED, migrate the local yaml to the canonical format:")
		fmt.Printf("  runos apps pull %s --force\n", configPath)
		fmt.Println("Then re-run `runos deploy`. The migration is one-time per yaml.")
		fmt.Println()
		fmt.Println("Other options:")
		fmt.Printf("  Inspect:       runos apps diff %s\n", configPath)
		fmt.Printf("  Deploy anyway: runos deploy --force   (keeps the shorthand; same drift\n")
		fmt.Println("                                        will reappear next deploy)")
	} else {
		fmt.Printf("Reconcile:  runos apps pull %s --force      (merge server state into your yaml first)\n", configPath)
		fmt.Printf("Inspect:    runos apps diff %s\n", configPath)
		fmt.Printf("Deploy anyway: runos deploy --force         (push your yaml; server state updates to match)\n")
	}
	return fmt.Errorf("upstream drift detected; pass --force to deploy anyway")
}

// deployDriftHeadline picks a directionally-correct headline for the
// deploy drift-gate refusal. Pre-fix the gate always said "the server
// has state your local yaml doesn't reflect", which was backwards or
// at best partial when the user had local additions ahead of the
// server (or both sides had unique state). Now:
//
//   - Pure server-additions (local⊆server, the most common case where
//     server-applied defaults landed after the last pull): clobber
//     warning unchanged.
//   - Mixed (both sides have unique state, e.g. local edited a value
//     the server also has but with a different value): "your yaml has
//     diverged from the server".
//
// Local-only additions (local⊇server) aren't blocking per
// NeedsForceToDeploy, so this helper is only reached for the two
// blocking cases.
//
// Regression target: I10-A.
func deployDriftHeadline(r *apps.DiffReport) string {
	yamlDirection := classifyDriftDirection(r.YAML)
	codeStale := r.Code.IsStale()
	switch {
	case yamlDirection == "server-additions" && !codeStale:
		return "the server has state your local yaml doesn't reflect."
	case yamlDirection == "mixed":
		return "your local yaml has diverged from the server."
	case codeStale && yamlDirection == "":
		return "newer source archives exist on the server than your recorded baseline."
	default:
		return "your local yaml has diverged from the server."
	}
}

// deployDriftHeadlineForce is the --force-pass-through variant of
// deployDriftHeadline. Same direction logic; phrased as a warning
// rather than a refusal headline.
func deployDriftHeadlineForce(r *apps.DiffReport) string {
	yamlDirection := classifyDriftDirection(r.YAML)
	switch yamlDirection {
	case "server-additions":
		return "Warning: server has changes your local yaml doesn't reflect, but --force was passed."
	case "mixed":
		return "Warning: your local yaml has diverged from the server, but --force was passed."
	default:
		return "Warning: drift detected, but --force was passed."
	}
}

// classifyDriftDirection inspects a SectionDiff's AdditiveOnly +
// LocalIsSuperset flags and returns "server-additions" (local⊆server),
// "local-additions" (server⊆local), "mixed" (neither subset), or ""
// (no drift). Used by the drift-gate headline so the user sees which
// side has the unique state.
func classifyDriftDirection(sd apps.SectionDiff) string {
	if sd.Status != apps.StatusDrift {
		return ""
	}
	switch {
	case sd.AdditiveOnly && !sd.LocalIsSuperset:
		return "server-additions"
	case !sd.AdditiveOnly && sd.LocalIsSuperset:
		return "local-additions"
	default:
		return "mixed"
	}
}
