package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/apps"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/git"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/services"

	"github.com/spf13/cobra"
)

var appsPullCmd = &cobra.Command{
	Use:   "pull [yaml-file]",
	Short: "Download running app config to local YAML files",
	Long: `Pull application configuration from a cluster to local YAML + env files.

Modes (precedence order, first match wins):

  1. <yaml-file> positional:  target = the yaml's directory.
  2. --all:                   pull every app in the cluster (each into its
                              own runos.<cid>.<id>/ subdir).
  3. --app-id <id>:           pull a specific app. With --out: flat into
                              that dir. Without --out: into ./runos.<cid>.<id>/.
  4. (none of the above):     auto-detect a runos*.yaml in the current
                              directory and use it as the positional yaml.
                              Errors out if zero or multiple candidates
                              are found.

Each app's directory contains the yaml plus its referenced files:

  runos.<cid>.<id>/
    runos.yaml                    # config (0644)
    .env                          # env vars (0600)
    .secret-files/<name>          # secret files (0600)
    overrides/<name>.yaml         # manifest overrides (0644)

Source code: pass --code to also pull the most recent CLI-deploy archive
into the same directory, or --code-version <cliUploadID> for a specific
older archive (see "runos apps list-previous-uploads"). --code is single-
app only, bulk runs (--all) do not pull code.

Code rollback workflow:

  runos apps list-previous-uploads <yaml-file>
  runos apps pull <yaml-file> --code-version <cliUploadID> --force
  cd <yaml's directory> && runos deploy

Safety: if any local file has been edited and would differ from the server
state, pull prints a diff and refuses to overwrite that app. Reconcile
your changes with "runos apps diff <yaml-file>", or re-run targeting that
single app with --force to discard local edits. --force always requires
an explicit target (yaml file or --app-id); bulk overwrites are blocked.

Examples:
  runos apps pull --all --cid mycluster3                                # every app in cluster
  runos apps pull --all --out ./synced --cid mycluster3                 # bulk into ./synced/
  runos apps pull runos.mycluster3.appid4/runos.yaml                     # re-pull the named yaml
  cd runos.mycluster3.appid4 && runos apps pull                          # auto-detect, re-pull in place
  cd runos.mycluster3.appid4 && runos apps pull --code                   # plus latest source
  runos apps pull --app-id appid4 --cid mycluster3                       # first-time pull (creates runos.mycluster3.appid4/)
  runos apps pull --app-id appid4 --out . --code                  # first-time, flat into cwd
  runos apps pull <yaml> --code-version 9e2c1f0b --force         # restore a specific source version`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAppsPull,
}

func init() {
	appsPullCmd.Flags().Bool("all", false, "pull every app in the cluster (required for bulk mode)")
	appsPullCmd.Flags().String("cid", "", "cluster ID (overrides default)")
	appsPullCmd.Flags().String("app-id", "", "app id to pull (alternative to passing a yaml file)")
	appsPullCmd.Flags().StringP("out", "o", "", "output directory: parent for --all (per-app subdirs appended), exact target for --app-id and yaml positional")
	appsPullCmd.Flags().BoolP("force", "f", false, "overwrite local files even when they have diverged from the server")
	appsPullCmd.Flags().BoolP("json", "j", false, "output pull summary as JSON")
	appsPullCmd.Flags().Bool("code", false, "also pull the source archive from the most recent CLI deploy (single-app only)")
	appsPullCmd.Flags().String("code-version", "", "pull a specific archive instead of the latest (cliUploadID; implies --code)")
	appsPullCmd.Flags().Bool("no-services", false, "skip pulling runos.service.<cid>.<sid>.yaml files for services referenced in requires:")
	appsPullCmd.Flags().Bool("keep-env", false, "preserve local env files (.runos.{cid}.{id}.env and runos.{cid}.{id}.config.env); pull won't overwrite them with server values")
	appsPullCmd.Flags().Bool("no-configpath-update", false, "skip auto-PATCHing the server-side configPath when a VCS app's local yaml lands at a different repo path; falls back to the existing stderr warning")
}

// pullSummary is the top-level JSON shape emitted by `apps pull --json`.
// One entry per pulled app in Apps; Drifted/Skipped capture apps the run
// touched but didn't fully complete (drift gate refused, or a per-app
// step failed).
type pullSummary struct {
	CID     string            `json:"cid"`
	Apps    []pulledAppEntry  `json:"apps"`
	Drifted []driftedAppEntry `json:"drifted,omitempty"`
	Skipped []pullSkipEntry   `json:"skipped,omitempty"`
}

// driftedAppEntry records an app whose local files diverged from the
// server. Pull refused to overwrite without --force; we surface it in the
// summary so users have a per-app command to copy.
type driftedAppEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// pulledAppEntry is the per-app JSON record in pullSummary.Apps. Counts
// (EnvVars, SecretFilesTotal/Written, OverridesTotal/Written) let callers
// distinguish "fully in-sync re-pull" from "wrote N new files" without
// reading the human-readable text.
type pulledAppEntry struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	YAML               apps.WriteResult  `json:"yaml"`
	SecretEnv          *apps.WriteResult `json:"secretEnv,omitempty"`
	Env                *apps.WriteResult `json:"env,omitempty"`
	// EnvVars is the count of plain user-set env vars — matches what
	// `apps_env-vars` returns. SecretEnvVars is the count of user-set
	// secret env vars (platform-injected keys from `requires:` aliases
	// are filtered out, since they're re-derived on every push).
	// Pre-fix this was a single `EnvVars` field summing both, which
	// double-counted requires-injected keys and disagreed with
	// `apps_env-vars` ground truth.
	EnvVars            int               `json:"envVarCount"`
	SecretEnvVars      int               `json:"secretEnvVarCount"`
	SecretFilesTotal   int               `json:"secretFilesTotal,omitempty"`
	SecretFilesWritten int               `json:"secretFilesWritten,omitempty"`
	OverridesTotal     int               `json:"overridesTotal,omitempty"`
	OverridesWritten   int               `json:"overridesWritten,omitempty"`
	Code               *pulledCodeEntry  `json:"code,omitempty"`
	// CodeVersion reports the local source-version sidecar's status
	// against the server's current archive list. Always populated when
	// a sidecar exists, even when --code wasn't passed this run, so
	// users get told "your code is N deploys behind" after a config-
	// only re-pull.
	CodeVersion *apps.CodeVersionStatus `json:"codeVersion,omitempty"`
	// Dockerignore is set when pull either wrote a default .dockerignore
	// (InSync=false) or detected an existing one and left it alone
	// (InSync=true). Omitted when EnsureDockerignore was not called for
	// this entry.
	Dockerignore *apps.WriteResult `json:"dockerignore,omitempty"`
	// ConfigPathUpdated records an auto-PATCH of the server-side configPath
	// during pull (V14 / V2 long-term fix). Populated only when pull issued
	// the PATCH and the server accepted it. Nil when nothing happened
	// (paths matched, non-VCS app, --no-configpath-update set, or PATCH
	// failed and we fell back to a stderr warning). MCP wrappers carry
	// this through unchanged so LLM-driven flows can detect the action
	// even though the warning never surfaces in their context.
	ConfigPathUpdated *configPathUpdate `json:"configPathUpdated,omitempty"`
}

// configPathUpdate is the structured shape of the auto-update event
// emitted on `pulledAppEntry`. Both fields are repo-relative paths.
type configPathUpdate struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// pulledCodeEntry summarises what the --code path did for one app.
type pulledCodeEntry struct {
	CliUploadID string `json:"cliUploadId"`
	PushTime    string `json:"pushTime,omitempty"`
	Size        int64  `json:"size,omitempty"`
	FilesWritten int   `json:"filesWritten"`
}

// allInSync is true when nothing on disk needed a change for this app:
// yaml + env both InSync (or env absent), zero writes happened to secret
// files or overrides, and we did NOT re-extract a source archive (a non-
// nil Code entry means the user passed --code or --code-version and we
// wrote a tarball's worth of files plus updated the sidecar).
func (e *pulledAppEntry) allInSync() bool {
	if !e.YAML.InSync {
		return false
	}
	if e.Env != nil && !e.Env.InSync {
		return false
	}
	if e.Code != nil {
		return false
	}
	return e.SecretFilesWritten == 0 && e.OverridesWritten == 0
}

// pullSkipEntry records an app (or a per-app step like --code) that was
// skipped along with a human-readable Reason. ID/Name may be empty when
// the skip happened before the app was identified.
type pullSkipEntry struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

func runAppsPull(cmd *cobra.Command, args []string) error {
	ctx, err := prepareAppsCmd(cmd)
	if err != nil {
		return err
	}

	all, _ := cmd.Flags().GetBool("all")
	outFlag, _ := cmd.Flags().GetString("out")
	force, _ := cmd.Flags().GetBool("force")
	appIDFlag, _ := cmd.Flags().GetString("app-id")
	codeFlag, _ := cmd.Flags().GetBool("code")
	codeVersion, _ := cmd.Flags().GetString("code-version")
	if codeVersion != "" {
		codeFlag = true
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	noServices, _ := cmd.Flags().GetBool("no-services")
	keepEnv, _ := cmd.Flags().GetBool("keep-env")
	noConfigPathUpdate, _ := cmd.Flags().GetBool("no-configpath-update")

	// --all and --app-id paths have no local yaml to source cid from, so
	// the user must have provided one explicitly. Yaml-positional and
	// auto-detect modes defer this check: cid is sourced from the yaml
	// after resolvePullPlan reads it.
	if all || appIDFlag != "" {
		if err := ctx.requireCID(); err != nil {
			return err
		}
	}

	plan, err := resolvePullPlan(args, all, appIDFlag, outFlag, ctx.cid, ctx.cfg.AccountID)
	if err != nil {
		return err
	}
	if plan.yamlCID != "" {
		if err := ctx.bindToYAML(plan.yamlCID); err != nil {
			return err
		}
	}
	if err := ctx.requireCID(); err != nil {
		return err
	}
	if err := validatePullPlan(plan, force, codeFlag); err != nil {
		return err
	}

	targets, err := resolvePullTargets(ctx.svc, plan.appID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if !jsonOutput {
			fmt.Printf("No apps found in cluster %s\n", ctx.cid)
		} else {
			emitJSON(pullSummary{CID: ctx.cid})
		}
		return nil
	}

	summary := pullSummary{CID: ctx.cid}
	driftAbortSeen := false

	// Services cascade machinery: lazily built on first hit so apps with
	// no requires don't pay the manifest-load cost.
	var (
		servicesExec     *dynacmd.Executor
		servicesManifest *manifest.Manifest
	)

	for _, t := range targets {
		// Server-controlled string flowing into a filesystem path —
		// reject anything outside the identifier alphabet so a
		// compromised conductor can't traverse out of the parent dir
		// via an id like "appid4/../../tmp/evil".
		if err := apps.ValidateIdentifier("app id", t.ID); err != nil {
			summary.Skipped = append(summary.Skipped, pullSkipEntry{
				ID:     t.ID,
				Name:   t.Name,
				Reason: err.Error(),
			})
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", displayTarget(t), err)
			}
			continue
		}
		appDir := plan.appDirFor(ctx.cid, t.ID)
		entry, skips, drifted, err := pullOne(ctx.svc, appDir, ctx.cid, ctx.cfg.AccountID, t, force, jsonOutput, codeFlag, codeVersion, keepEnv, noConfigPathUpdate, plan.forceSuffixedYaml(), plan.defaultSourceDir())
		if err != nil {
			summary.Skipped = append(summary.Skipped, pullSkipEntry{
				ID:     t.ID,
				Name:   t.Name,
				Reason: err.Error(),
			})
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", displayTarget(t), err)
			}
			continue
		}
		summary.Skipped = append(summary.Skipped, skips...)
		if !jsonOutput {
			for _, sk := range skips {
				fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", displayTarget(t), sk.Reason)
			}
		}
		if drifted {
			driftAbortSeen = true
			summary.Drifted = append(summary.Drifted, driftedAppEntry{ID: t.ID, Name: t.Name})
			continue
		}
		summary.Apps = append(summary.Apps, *entry)

		// Services cascade: pull a runos.service.<cid>.<sid>.yaml for
		// every requires entry whose service id is set and whose yaml
		// isn't already on disk. Lazy-build the executor + manifest
		// the first time we hit this path so pulls without requires
		// don't pay the cost.
		if !noServices {
			if servicesExec == nil {
				servicesExec = dynacmd.NewExecutor(ctx.cfg.GetAPIURL())
			}
			if servicesManifest == nil {
				m, mErr := loadLocalManifest(ctx.cfg.GetAPIURL())
				if mErr != nil {
					if !jsonOutput {
						fmt.Fprintf(os.Stderr, "Warning: services cascade disabled: %v\n", mErr)
					}
					noServices = true
				} else {
					servicesManifest = m
				}
			}
			if servicesManifest != nil {
				casSkips := cascadePulledServices(servicesExec, servicesManifest, appDir, ctx.cid, ctx.cfg.AccountID, t.ID)
				summary.Skipped = append(summary.Skipped, casSkips...)
				if !jsonOutput {
					for _, sk := range casSkips {
						fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", displayTarget(t), sk.Reason)
					}
				}
			}
		}
	}

	if jsonOutput {
		if err := emitJSON(summary); err != nil {
			return err
		}
	} else {
		// Inline drift blocks above end without a trailing blank line, so add
		// one before the rollup summary if any drift was reported.
		if driftAbortSeen {
			fmt.Println()
		}
		printPullSummary(summary)
	}

	if driftAbortSeen {
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		return fmt.Errorf("one or more apps had local drift; re-run with --force to overwrite")
	}
	return nil
}

// pullPlan captures the resolved mode + per-target write location for a
// single invocation of `apps pull`. mode is one of: "bulk", "yaml" (the
// yaml's directory anchors the target), "id-flat" (--app-id with --out
// = exact target), "id-subdir" (--app-id without --out = ./runos.<cid>.<id>/).
type pullPlan struct {
	mode       string
	appID      string // empty for bulk
	bulkParent string // for bulk only: parent dir in which per-app subdirs are created
	fixedDir   string // for single modes: the resolved appDir for the one target
	yamlCID    string // populated in yaml-positional / auto-detect modes; empty otherwise
}

// appDirFor returns the destination directory for a given resolved
// target. For bulk it appends the per-app subdir; for single modes the
// fixedDir is used as-is (and target is ignored).
func (p pullPlan) appDirFor(cid, appID string) string {
	if p.mode == "bulk" {
		return filepath.Join(p.bulkParent, apps.DefaultBaseName(cid, appID))
	}
	return p.fixedDir
}

// defaultSourceDir is the sourceDir value pullOne should stamp on a
// freshly-pulled yaml when the existing local yaml (if any) doesn't
// already pin a value. ".." for subdir modes (bulk, id-subdir) where the
// user is opting into directory-per-app and source code lives at the
// parent. Empty for flat modes (id-flat, yaml-anchored) because the
// destination is the user's explicit choice and we can't infer where
// their source tree is. Re-pulls preserve any existing sourceDir over
// this default.
func (p pullPlan) defaultSourceDir() string {
	switch p.mode {
	case "bulk", "id-subdir":
		return ".."
	default:
		return ""
	}
}

// forceSuffixedYaml reports whether pullOne should bypass YAMLFilename's
// canonical-on-empty-dir resolution and write straight to the per-app
// suffixed leaf. True for `id-flat` mode (--app-id + --out, the
// LLM/CI fan-out shape) where multiple concurrent pulls share the target
// directory. Without this hint, the V1 race lets the first writer claim
// the canonical runos.yaml slot non-deterministically. Other modes
// (yaml-positional, bulk, id-subdir) either have a single yaml in the
// target dir or get their own per-app subdir, so the race can't occur
// and the canonical name is preserved for re-pulls.
func (p pullPlan) forceSuffixedYaml() bool {
	return p.mode == "id-flat"
}

// resolvePullPlan turns command-line args + flags into a pullPlan. It
// also performs all input-side validation that doesn't require a server
// roundtrip: mutually-exclusive flag combinations, yaml/cid/aid sanity
// checks, and the auto-detect fallback for "no positional, no --app-id,
// no --all".
//
// expectedCID is used for two purposes: as the dir name fragment in
// `id-subdir` mode (where there's no yaml to source the cid from) and
// as the cross-check value for yaml-positional / auto-detect modes
// (when non-empty). Pass "" to skip the cross-check; the caller binds
// to plan.yamlCID afterwards.
func resolvePullPlan(args []string, all bool, appIDFlag, outFlag, expectedCID, expectedAID string) (pullPlan, error) {
	if all && len(args) > 0 {
		return pullPlan{}, fmt.Errorf("--all and a positional yaml file are mutually exclusive")
	}
	if all && appIDFlag != "" {
		return pullPlan{}, fmt.Errorf("--all and --app-id are mutually exclusive")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return pullPlan{}, fmt.Errorf("failed to get current directory: %w", err)
	}

	// --all: bulk mode. parent = --out or cwd; per-app subdirs added in the loop.
	if all {
		parent := outFlag
		if parent == "" {
			parent = cwd
		}
		return pullPlan{mode: "bulk", bulkParent: parent}, nil
	}

	// Positional yaml takes precedence over --app-id auto-detect.
	yamlPath := ""
	if len(args) == 1 {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return pullPlan{}, fmt.Errorf("resolve yaml path: %w", err)
		}
		yamlPath = abs
	} else if appIDFlag == "" {
		// No positional, no --app-id, no --all: auto-detect from cwd.
		scan, err := apps.FindPulledYAMLs(cwd)
		if err != nil {
			return pullPlan{}, fmt.Errorf("scan cwd for yaml: %w", err)
		}
		switch {
		case len(scan.Valid) == 1:
			yamlPath = scan.Valid[0]
		case len(scan.Valid) > 1:
			return pullPlan{}, fmt.Errorf(
				"multiple yaml candidates in current directory: %s, pass one explicitly or use --all",
				strings.Join(relativisePaths(cwd, scan.Valid), ", "),
			)
		case len(scan.Partial) > 0:
			return pullPlan{}, fmt.Errorf(
				"found %s but missing id/cid/aid, looks like a fresh deploy yaml, not a pulled one. Run 'runos apps pull --app-id <id>' to fetch a pulled yaml, or pass --app-id directly",
				strings.Join(relativisePaths(cwd, scan.Partial), ", "),
			)
		default:
			return pullPlan{}, fmt.Errorf(
				"no runos*.yaml found in current directory. Pass a yaml file, --app-id <id>, or --all",
			)
		}
	}

	if yamlPath != "" {
		localApp, err := apps.LoadLocalApp(yamlPath)
		if err != nil {
			if os.IsNotExist(err) {
				return pullPlan{}, fmt.Errorf("yaml file %q not found", yamlPath)
			}
			return pullPlan{}, fmt.Errorf("read yaml: %w", err)
		}
		if localApp.ID == "" || localApp.CID == "" || localApp.AID == "" {
			return pullPlan{}, fmt.Errorf("yaml at %q is missing id/cid/aid, pull again from the server to refresh", yamlPath)
		}
		// Defence-in-depth: ids flow into filesystem paths (per-app dir,
		// env filename) and into URLs. Reject anything outside the
		// conductor identifier alphabet so a tampered yaml can't
		// traverse out of the parent dir.
		if err := apps.ValidateIdentifier("app id", localApp.ID); err != nil {
			return pullPlan{}, fmt.Errorf("yaml at %q: %w", yamlPath, err)
		}
		if err := apps.ValidateIdentifier("cluster id", localApp.CID); err != nil {
			return pullPlan{}, fmt.Errorf("yaml at %q: %w", yamlPath, err)
		}
		if localApp.AID != expectedAID {
			return pullPlan{}, fmt.Errorf("yaml is for account %q but you're logged in as %q", localApp.AID, expectedAID)
		}
		if expectedCID != "" && localApp.CID != expectedCID {
			return pullPlan{}, fmt.Errorf("cluster mismatch: yaml is for cluster %q but --cid (or default) is %q", localApp.CID, expectedCID)
		}
		if appIDFlag != "" && appIDFlag != localApp.ID {
			return pullPlan{}, fmt.Errorf("--app-id %q doesn't match the yaml's id %q", appIDFlag, localApp.ID)
		}
		fixed := outFlag
		if fixed == "" {
			fixed = filepath.Dir(yamlPath)
		}
		return pullPlan{mode: "yaml", appID: localApp.ID, fixedDir: fixed, yamlCID: localApp.CID}, nil
	}

	// --app-id without yaml. With --out: flat into that dir. Without --out:
	// per-app subdir in cwd (matches the bulk default for a single app).
	if err := apps.ValidateIdentifier("app id", appIDFlag); err != nil {
		return pullPlan{}, err
	}
	if outFlag != "" {
		return pullPlan{mode: "id-flat", appID: appIDFlag, fixedDir: outFlag}, nil
	}
	return pullPlan{mode: "id-subdir", appID: appIDFlag, fixedDir: filepath.Join(cwd, apps.DefaultBaseName(expectedCID, appIDFlag))}, nil
}

// validatePullPlan checks plan-level invariants that depend on flags
// other than the ones already encoded in resolvePullPlan (force, code).
func validatePullPlan(plan pullPlan, force, codeFlag bool) error {
	if force && plan.mode == "bulk" {
		return fmt.Errorf("--force requires a single-app target (yaml file or --app-id); bulk overwrites are intentionally blocked")
	}
	if codeFlag && plan.mode == "bulk" {
		return fmt.Errorf("--code requires a single-app target (yaml file or --app-id); bulk code pulls are intentionally blocked")
	}
	return nil
}

// resolvePullTargets returns the list of apps to pull. Empty appID means
// "every app in the cluster" (bulk); a non-empty id resolves to a single
// app or an error if no match.
func resolvePullTargets(svc *apps.Service, appID string) ([]apps.AppSummary, error) {
	list, err := svc.ListApps()
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}
	if appID == "" {
		return list, nil
	}
	for _, a := range list {
		if a.ID == appID {
			return []apps.AppSummary{a}, nil
		}
	}
	return nil, fmt.Errorf("no app with id %q in this cluster", appID)
}

// pickSourceDir returns the sourceDir the next save should stamp onto
// the yaml. Priority: local yaml (re-pulls don't clobber user edits) >
// server (V13: AppDocument round-trips sourceDir so fresh checkouts get
// a complete yaml) > caller's default.
func pickSourceDir(yamlPath, serverSourceDir, defaultSourceDir string) string {
	if existing, err := apps.LoadLocalApp(yamlPath); err == nil && existing.SourceDir != "" {
		return existing.SourceDir
	}
	if serverSourceDir != "" {
		return serverSourceDir
	}
	return defaultSourceDir
}

// pickDockerfile returns the dockerfile the next save should stamp onto
// the yaml. Same priority rules as pickSourceDir; default is "" (yaml
// omits the field via omitempty when both local and server are empty,
// keeping single-app-at-root layouts clean).
func pickDockerfile(yamlPath, serverDockerfile string) string {
	if existing, err := apps.LoadLocalApp(yamlPath); err == nil && existing.Dockerfile != "" {
		return existing.Dockerfile
	}
	return serverDockerfile
}

// pullOne fetches everything the server holds for target, compares it
// against any local files inside appDir, and either writes or aborts.
//
// appDir is the resolved per-app destination directory: the yaml lives
// directly inside it, alongside .env, .secret-files/, overrides/, and
// (when --code) the extracted source. The caller is responsible for
// computing this from the user's mode (yaml-anchored, --app-id, --all).
//
// Flow:
//  1. Fetch raw app, env vars, secret-file list (metadata), overrides (with content).
//  2. Build the PulledApp that a fresh pull would produce.
//  3. Diff against local files.
//  4. If anything has *drifted* (local bytes differ from server) and force is
//     false, render the diff and return drifted=true without touching disk.
//  5. Otherwise write everything that isn't already in sync. Secret-file
//     content is fetched only for entries that need writing.
//
// Per-file failures (network blips, decode errors) become skips and don't
// abort the whole app.
//
// defaultSourceDir is the sourceDir to stamp on the saved yaml when no
// existing local yaml pins one (typically ".." for subdir-mode pulls,
// empty otherwise; see pullPlan.defaultSourceDir).
func pullOne(svc *apps.Service, appDir, cid, aid string, target apps.AppSummary, force, jsonOutput, codeFlag bool, codeVersion string, keepEnv, noConfigPathUpdate, forceSuffixedYaml bool, defaultSourceDir string) (*pulledAppEntry, []pullSkipEntry, bool, error) {
	raw, err := svc.GetApp(target.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("fetch app: %w", err)
	}

	secretEnvVars, err := svc.GetAppSecretEnvVars(target.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("fetch secret env vars: %w", err)
	}
	envVars, envErr := svc.GetAppEnvVars(target.ID)
	if envErr != nil {
		return nil, nil, false, fmt.Errorf("fetch env vars: %w", envErr)
	}

	secretList, err := svc.ListSecretFiles(target.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list secret files: %w", err)
	}

	overrideList, err := svc.ListOverrides(target.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("list overrides: %w", err)
	}

	requiresMap, err := svc.GetAppRequires(target.ID)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read requires: %w", err)
	}

	serverState := apps.BuildServerStateForDiff(raw, cid, aid, secretEnvVars, envVars, secretList, overrideList, requiresMap)
	if serverState.App == "" {
		serverState.App = target.Name
	}
	if serverState.ID == "" {
		serverState.ID = target.ID
	}
	if len(serverState.ServicePortMappings) == 0 && target.Port > 0 {
		serverState.ServicePortMappings = []apps.Port{{Port: target.Port, StandardHttps: true}}
	}

	// Compute the same diff the `apps diff` command produces so we can
	// gate the write on it and reuse the on-drift render. The leaf name
	// matches whichever runos*.yaml the upcoming SaveYAML will write,
	// so the drift check reads the same file the write will overwrite.
	// V1: in id-flat mode (parallel-pull-prone --app-id + --out shape)
	// the upcoming write goes through SaveYAMLSuffixed which always
	// targets the per-app suffixed leaf; mirror that here so the diff
	// reads the same file the write will land in.
	var yamlLeaf string
	if forceSuffixedYaml {
		yamlLeaf = apps.SuffixedYAMLFilename(cid, target.ID)
	} else {
		yamlLeaf, err = apps.YAMLFilename(appDir, cid, target.ID)
		if err != nil {
			return nil, nil, false, fmt.Errorf("resolve yaml filename: %w", err)
		}
	}
	yamlPath := filepath.Join(appDir, yamlLeaf)

	// /requires is server-authoritative for type/id/config/env;
	// only Class is local-only. The merge also handles legacy apps
	// (deployed before /requires landed) that return empty
	// Config/Env, by falling back to the local yaml's values.
	if existing, lerr := apps.LoadLocalApp(yamlPath); lerr == nil {
		apps.MergeRequiresUserAuthored(serverState, existing)
	}

	yamlDiff, err := apps.ComputeYAMLDiff(yamlPath, serverState)
	if err != nil {
		return nil, nil, false, fmt.Errorf("yaml diff: %w", err)
	}
	secretEnvDiff := apps.SectionDiff{Status: apps.StatusInSync}
	if len(secretEnvVars) > 0 {
		secretEnvPath := filepath.Join(appDir, apps.SecretEnvFilename(cid, target.ID))
		secretEnvDiff, err = apps.ComputeEnvDiff(secretEnvPath, secretEnvVars)
		if err != nil {
			return nil, nil, false, fmt.Errorf("secret env diff: %w", err)
		}
	}
	envDiff := apps.SectionDiff{Status: apps.StatusInSync}
	if len(envVars) > 0 {
		envPath := filepath.Join(appDir, apps.EnvFilename(cid, target.ID))
		envDiff, err = apps.ComputeEnvDiff(envPath, envVars)
		if err != nil {
			return nil, nil, false, fmt.Errorf("env diff: %w", err)
		}
	}
	secretFilesDiff := apps.SecretFilesDiff{Status: apps.StatusInSync}
	if len(secretList) > 0 {
		secretsDir := filepath.Join(appDir, apps.SecretFilesDirname())
		paths := make(map[string]string, len(secretList))
		for _, sf := range secretList {
			paths[sf.Filename] = filepath.Join(secretsDir, sf.Filename)
		}
		secretFilesDiff, err = apps.ComputeSecretFilesDiff(paths, secretList)
		if err != nil {
			return nil, nil, false, fmt.Errorf("secret files diff: %w", err)
		}
	}
	overridesDiff := apps.OverridesDiff{Status: apps.StatusInSync}
	if len(overrideList) > 0 {
		overridesDir := filepath.Join(appDir, apps.OverridesDirname())
		filenames := apps.OverrideFilenames(overrideList)
		paths := make(map[string]string, len(overrideList))
		for i, o := range overrideList {
			paths[o.ID] = filepath.Join(overridesDir, filenames[i])
		}
		overridesDiff, err = apps.ComputeOverridesDiff(paths, overrideList)
		if err != nil {
			return nil, nil, false, fmt.Errorf("overrides diff: %w", err)
		}
	}

	report := &apps.DiffReport{
		CID: cid, AppID: serverState.ID, AppName: serverState.App,
		YAML:        yamlDiff,
		SecretEnv:   secretEnvDiff,
		Env:         envDiff,
		SecretFiles: secretFilesDiff,
		Overrides:   overridesDiff,
	}

	if report.NeedsForceToOverwrite() && !force {
		if !jsonOutput {
			fmt.Printf("\n%s (%s) on cluster %s, local edits would be lost. Refusing to overwrite.\n", serverState.App, serverState.ID, cid)
			printDiffReport(report)
			fmt.Println()
			fmt.Printf("Review:    runos apps diff --app-id %s --cid %s\n", serverState.ID, cid)
			fmt.Printf("Overwrite: runos apps pull --app-id %s --cid %s --force\n", serverState.ID, cid)
		}
		return nil, nil, true, nil
	}

	// Safe to write. yaml and env go through their shared helpers;
	// secret files and overrides are written only where not already in sync.
	// We pass appDir as the parent and an empty base so the helpers write
	// directly into appDir (no extra subdir wrapping).
	var skips []pullSkipEntry

	// Build-metadata round-trip (V13). Priority: local yaml > server >
	// caller's default. BuildPulledApp already populated serverState
	// with whatever the AppDocument carries; here we let any existing
	// local yaml win (re-pulls don't clobber user edits) and fall back
	// to the caller-specified default (".." for subdir modes, "" for
	// flat modes) only when both local and server are empty.
	serverState.SourceDir = pickSourceDir(yamlPath, serverState.SourceDir, defaultSourceDir)
	serverState.Dockerfile = pickDockerfile(yamlPath, serverState.Dockerfile)

	// V1: id-flat mode goes through SaveYAMLSuffixed so concurrent pulls
	// into a shared --out dir don't race for the canonical runos.yaml
	// slot. Other modes keep SaveYAML's resolve-on-write logic, which
	// preserves the canonical name on single-app re-pulls.
	var yamlRes apps.WriteResult
	if forceSuffixedYaml {
		yamlRes, err = apps.SaveYAMLSuffixed(appDir, "", serverState)
	} else {
		yamlRes, err = apps.SaveYAML(appDir, "", serverState)
	}
	if err != nil {
		return nil, skips, false, fmt.Errorf("save yaml: %w", err)
	}

	// Filter platform-injected secret env vars (values claimed by
	// `requires.<alias>.env` mappings such as DATABASE_URL, CACHE_URL)
	// out of the user-facing count. Pre-fix this summed plain + secret
	// without filtering, so the count was inflated AND disagreed with
	// what `apps_env-vars` returned. Now `envVarCount` matches
	// `apps_env-vars` keys exactly, and `secretEnvVarCount` reflects
	// user-set secret keys only.
	injected := apps.FindServerInjectedEnvCollisions(secretEnvVars, serverState.Requires)
	userSecretCount := len(secretEnvVars) - len(injected)
	if userSecretCount < 0 {
		userSecretCount = 0
	}

	entry := &pulledAppEntry{
		ID:               serverState.ID,
		Name:             serverState.App,
		YAML:             yamlRes,
		EnvVars:          len(envVars),
		SecretEnvVars:    userSecretCount,
		SecretFilesTotal: len(serverState.SecretFiles),
		OverridesTotal:   len(serverState.Overrides),
	}

	// V14 / long-term V2: auto-update server-side configPath when a VCS
	// app's local yaml lands at a different repo-relative path than the
	// AppDocument remembers. Without this, the next VCS deploy would fail
	// at the cluster-agent fetch step because the committed tree no
	// longer has the yaml at the stored path. The decision is a pure
	// helper (apps.DecideConfigPathAction); the dispatch is a thin switch
	// on its return.
	serverConfigPath, _ := raw["configPath"].(string)
	localRepoRelPath := vcsRepoRelPath(yamlRes.Path)
	switch apps.DecideConfigPathAction(serverConfigPath, localRepoRelPath, serverState.DeployType, noConfigPathUpdate) {
	case apps.ConfigPathActionSkip:
		// Nothing to do (non-VCS, paths match, or caller couldn't compute).
	case apps.ConfigPathActionUpdate:
		// PATCH the server's configPath to match the new local path.
		// On success, stamp the structured event onto the entry so the
		// JSON consumer (and the MCP wrapper that inherits the field
		// for free) sees the action even when stderr is silenced. On
		// failure, fall through to the Warn shape so the user still
		// gets the corrective hint and pull doesn't abort.
		if _, patchErr := svc.UpdateApp(target.ID, map[string]any{"configPath": localRepoRelPath}); patchErr != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: auto-update of server configPath failed (%v); %s\n",
					patchErr, apps.ConfigPathMismatchWarning(serverConfigPath, localRepoRelPath, "vcs"))
			}
		} else {
			entry.ConfigPathUpdated = &configPathUpdate{From: serverConfigPath, To: localRepoRelPath}
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "configPath: updated server-side from %q to %q so the next VCS deploy reads the right yaml.\n",
					serverConfigPath, localRepoRelPath)
			}
		}
	case apps.ConfigPathActionWarn:
		// Explicit opt-out (--no-configpath-update). Print the existing
		// V2-shape warning so the user knows what's drifting and how to
		// fix it manually.
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: %s\n",
				apps.ConfigPathMismatchWarning(serverConfigPath, localRepoRelPath, "vcs"))
		}
	}

	// --keep-env: skip writing both env files so user-edited local content
	// (typically dev-only overrides on a working app) isn't silently
	// clobbered by server values. The --force gate already lets a pull
	// proceed past drift; --keep-env is the surgical opt-out for the env
	// side specifically. Stdout shows the kept paths so the user knows
	// nothing was rewritten and can run apps_diff to see the divergence.
	if keepEnv {
		if !jsonOutput {
			if len(secretEnvVars) > 0 {
				fmt.Printf("  secret env vars: kept local %s (--keep-env, server has %d key(s))\n",
					apps.SecretEnvFilename(cid, target.ID), len(secretEnvVars))
			}
			if len(envVars) > 0 {
				fmt.Printf("  plain env vars:  kept local %s (--keep-env, server has %d key(s))\n",
					apps.EnvFilename(cid, target.ID), len(envVars))
			}
		}
	} else {
		if len(secretEnvVars) > 0 {
			secretEnvRes, err := apps.SaveSecretEnv(appDir, "", cid, target.ID, secretEnvVars)
			if err != nil {
				return entry, skips, false, fmt.Errorf("save secret env: %w", err)
			}
			entry.SecretEnv = &secretEnvRes
		}
		if len(envVars) > 0 {
			envRes, err := apps.SaveEnv(appDir, "", cid, target.ID, envVars)
			if err != nil {
				return entry, skips, false, fmt.Errorf("save env: %w", err)
			}
			entry.Env = &envRes
		}
	}

	secretsWritten, secretSkips := writeSecretFilesNeedingUpdate(svc, appDir, target.ID, serverState, secretFilesDiff)
	overridesWritten, overrideSkips := writeOverridesNeedingUpdate(appDir, serverState, overrideList, overridesDiff)
	entry.SecretFilesWritten = secretsWritten
	entry.OverridesWritten = overridesWritten
	skips = append(skips, secretSkips...)
	skips = append(skips, overrideSkips...)

	if codeFlag {
		codeEntry, codeSkip := pullCode(svc, target.ID, cid, appDir, codeVersion)
		if codeSkip != nil {
			codeSkip.ID = serverState.ID
			codeSkip.Name = serverState.App
			skips = append(skips, *codeSkip)
		}
		entry.Code = codeEntry
	}

	// Populate the code-version status (sidecar vs server archives)
	// regardless of --code, so a config-only re-pull surfaces "your
	// source is N deploys behind" without the user asking. Best-effort:
	// errors are non-fatal and just leave the field nil.
	if status, statusErr := apps.ComputeCodeVersionStatus(svc, cid, target.ID, appDir); statusErr == nil {
		entry.CodeVersion = status
	}

	// Drop a default .dockerignore so external Docker builders honour the
	// same RunOS-file exclusions the CLI tarball walker enforces. Run last
	// so any .dockerignore extracted from a source archive (--code flow)
	// is preserved over the default. Best-effort: a write failure here
	// turns into a stderr warning, the rest of the pull still succeeds.
	if dockerRes, derr := apps.EnsureDockerignore(appDir); derr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not write .dockerignore in %s: %v\n", appDir, derr)
	} else {
		entry.Dockerignore = &dockerRes
	}

	return entry, skips, false, nil
}

// pullCode resolves the target archive (latest if codeVersion is empty),
// streams it down, and extracts into appDir. Returns nil entry + a
// non-nil skip on any failure short of "no archives recorded" (which is
// a non-error skip in its own right).
func pullCode(svc *apps.Service, appID, cid, appDir, codeVersion string) (*pulledCodeEntry, *pullSkipEntry) {
	target, err := resolveCodeArchive(svc, appID, codeVersion)
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: %v", err)}
	}
	if target == nil {
		return nil, &pullSkipEntry{Reason: "code: no CLI uploads recorded for this app"}
	}

	body, err := mintAndDownload(context.Background(), svc, appID, target.CliUploadID)
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: %v", err)}
	}
	defer body.Close()

	if err := os.MkdirAll(appDir, 0755); err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: mkdir %s: %v", appDir, err)}
	}
	written, err := apps.ExtractTarGz(body, appDir, apps.PulledCodeSkipPaths(cid, appID))
	if err != nil {
		return nil, &pullSkipEntry{Reason: fmt.Sprintf("code: extract: %v", err)}
	}
	// Record which archive this directory's source comes from so the
	// pre-deploy gate can detect upstream deploys that landed after
	// this pull. Failure here is non-fatal, the user still has the
	// extracted code; only drift detection is degraded.
	if err := apps.WriteSourceVersion(appDir, cid, appID, target.CliUploadID); err != nil {
		return &pulledCodeEntry{
			CliUploadID:  target.CliUploadID,
			PushTime:     target.PushTime,
			Size:         target.Size,
			FilesWritten: written,
		}, &pullSkipEntry{Reason: fmt.Sprintf("code: record source version: %v", err)}
	}
	return &pulledCodeEntry{
		CliUploadID:  target.CliUploadID,
		PushTime:     target.PushTime,
		Size:         target.Size,
		FilesWritten: written,
	}, nil
}

// resolveCodeArchive returns the chosen archive. Empty codeVersion picks
// the most recent by pushTime; an explicit codeVersion is matched against
// the listing. Returns (nil, nil) when the listing is empty, caller
// distinguishes "no archives" from genuine errors.
func resolveCodeArchive(svc *apps.Service, appID, codeVersion string) (*apps.CliArchive, error) {
	list, err := svc.ListCliArchives(appID)
	if err != nil {
		return nil, fmt.Errorf("list archives: %w", err)
	}
	if len(list) == 0 {
		return nil, nil
	}
	if codeVersion == "" {
		latest := list[0]
		for _, a := range list[1:] {
			if a.PushTime > latest.PushTime {
				latest = a
			}
		}
		return &latest, nil
	}
	for i := range list {
		if list[i].CliUploadID == codeVersion {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no archive with cliUploadID %q (run 'runos apps list-previous-uploads' to see what's available)", codeVersion)
}

// mintAndDownload mints a single-use ticket and streams the archive.
// Retries once on a 401 (token expired or already used) by minting a
// fresh ticket; two consecutive 401s are surfaced as an error.
func mintAndDownload(ctx context.Context, svc *apps.Service, appID, cliUploadID string) (io.ReadCloser, error) {
	for range 2 {
		ticket, err := svc.PrepareCliPull(appID, cliUploadID, 0)
		if err != nil {
			return nil, fmt.Errorf("mint download URL: %w", err)
		}
		body, err := svc.DownloadCliArchive(ctx, ticket.DownloadURL)
		if err == nil {
			return body, nil
		}
		if !errors.Is(err, apps.ErrTicketConsumed) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("download URL kept reporting expired/consumed; aborting")
}

// writeSecretFilesNeedingUpdate fetches content + writes for any secret
// file whose local state isn't already in_sync. Files already matching the
// server md5 are skipped, saving a GetSecretFile call each. Returns the
// number of files actually written so the summary can distinguish written
// vs in-sync.
func writeSecretFilesNeedingUpdate(svc *apps.Service, appDir, appID string, serverState *apps.PulledApp, diff apps.SecretFilesDiff) (written int, skips []pullSkipEntry) {
	if diff.Status == apps.StatusInSync {
		return 0, nil
	}
	needs := map[string]bool{}
	for _, e := range diff.Entries {
		if e.Status != apps.StatusInSync {
			needs[e.Filename] = true
		}
	}
	for _, sf := range serverState.SecretFiles {
		if !needs[sf.Filename] {
			continue
		}
		content, err := svc.GetSecretFile(appID, sf.Filename)
		if err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     serverState.ID,
				Name:   serverState.App,
				Reason: fmt.Sprintf("secret file %q: fetch: %v", sf.Filename, err),
			})
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(content.Content)
		if err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     serverState.ID,
				Name:   serverState.App,
				Reason: fmt.Sprintf("secret file %q: base64 decode: %v", sf.Filename, err),
			})
			continue
		}
		if _, err := apps.SaveSecretFile(appDir, "", sf.Filename, decoded); err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     serverState.ID,
				Name:   serverState.App,
				Reason: fmt.Sprintf("secret file %q: %v", sf.Filename, err),
			})
			continue
		}
		written++
	}
	return written, skips
}

// writeOverridesNeedingUpdate writes any override whose local state isn't
// in_sync. The list endpoint already returned content so no extra fetch
// is required. Returns the number of overrides actually written.
func writeOverridesNeedingUpdate(appDir string, serverState *apps.PulledApp, overrideList []apps.OverrideSummary, diff apps.OverridesDiff) (written int, skips []pullSkipEntry) {
	if diff.Status == apps.StatusInSync {
		return 0, nil
	}
	needs := map[string]bool{}
	for _, e := range diff.Entries {
		if e.Status != apps.StatusInSync {
			needs[e.ID] = true
		}
	}
	filenames := apps.OverrideFilenames(overrideList)
	for i, o := range overrideList {
		if !needs[o.ID] {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(o.Data)
		if err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     serverState.ID,
				Name:   serverState.App,
				Reason: fmt.Sprintf("override %q: base64 decode: %v", o.Name, err),
			})
			continue
		}
		if _, err := apps.SaveOverride(appDir, "", filenames[i], decoded); err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     serverState.ID,
				Name:   serverState.App,
				Reason: fmt.Sprintf("override %q: %v", o.Name, err),
			})
			continue
		}
		written++
	}
	return written, skips
}
func printPullSummary(s pullSummary) {
	if len(s.Apps) == 0 && len(s.Skipped) == 0 && len(s.Drifted) == 0 {
		fmt.Printf("No apps pulled from cluster %s\n", s.CID)
		return
	}

	var updated, inSync []pulledAppEntry
	for _, a := range s.Apps {
		if a.allInSync() {
			inSync = append(inSync, a)
		} else {
			updated = append(updated, a)
		}
	}

	if len(updated) > 0 {
		fmt.Printf("Updated %d app(s) on cluster %s:\n", len(updated), s.CID)
		for _, a := range updated {
			fmt.Println()
			printUpdatedApp(a)
		}
	}

	if len(inSync) > 0 {
		if len(updated) > 0 {
			fmt.Println()
		}
		fmt.Printf("Already in sync, no changes written (%d app(s)):\n", len(inSync))
		for _, a := range inSync {
			fmt.Printf("  %s (%s)%s\n", a.Name, a.ID, inSyncDetail(a))
			if note := codeStaleNote(a.CodeVersion); note != "" {
				fmt.Printf("    %s\n", note)
			}
		}
	}

	if len(s.Drifted) > 0 {
		if len(updated) > 0 || len(inSync) > 0 {
			fmt.Println()
		}
		fmt.Printf("Skipped due to local drift (%d app(s)). Re-run with --force to overwrite:\n", len(s.Drifted))
		for _, d := range s.Drifted {
			fmt.Printf("  %s (%s) → runos apps pull %s --cid %s --force\n", d.Name, d.ID, d.ID, s.CID)
		}
	}

	if len(s.Skipped) > 0 {
		fmt.Printf("\nSkipped %d:\n", len(s.Skipped))
		for _, sk := range s.Skipped {
			name := sk.Name
			if sk.ID != "" {
				name = fmt.Sprintf("%s (%s)", sk.Name, sk.ID)
			}
			fmt.Printf("  %s: %s\n", name, sk.Reason)
		}
	}
}

// printUpdatedApp renders an app that had at least one file change.
func printUpdatedApp(a pulledAppEntry) {
	fmt.Printf("  %s (%s)\n", a.Name, a.ID)
	fmt.Printf("    yaml: %s (%s)\n", a.YAML.Path, writeStateLabel(a.YAML))
	if a.Env != nil {
		fmt.Printf("    env:        %s (%s, %d vars)\n", a.Env.Path, writeStateLabel(*a.Env), a.EnvVars)
	}
	if a.SecretEnv != nil {
		fmt.Printf("    secret env: %s (%s, %d vars)\n", a.SecretEnv.Path, writeStateLabel(*a.SecretEnv), a.SecretEnvVars)
	}
	if a.SecretFilesTotal > 0 {
		fmt.Printf("    secretFiles: %s\n", writtenInSyncLabel(a.SecretFilesWritten, a.SecretFilesTotal-a.SecretFilesWritten))
	}
	if a.OverridesTotal > 0 {
		fmt.Printf("    overrides:   %s\n", writtenInSyncLabel(a.OverridesWritten, a.OverridesTotal-a.OverridesWritten))
	}
	if a.Code != nil {
		fmt.Printf("    code: %s (%d files extracted from cliUploadID %s)\n", humanSize(a.Code.Size), a.Code.FilesWritten, a.Code.CliUploadID)
	}
	if note := codeStaleNote(a.CodeVersion); note != "" {
		fmt.Printf("    %s\n", note)
	}
}

// codeStaleNote returns a one-line warning when the local source-version
// sidecar is behind the server. Empty string when there's no sidecar
// (no baseline) or the source is up-to-date.
func codeStaleNote(s *apps.CodeVersionStatus) string {
	if s == nil || !s.HasBaseline() {
		return ""
	}
	if !s.RecordedFound {
		return fmt.Sprintf("note: recorded source version %s isn't in the server's archive list", s.Recorded)
	}
	if !s.IsStale() {
		return ""
	}
	return fmt.Sprintf("note: %d newer source archive(s) on server (run 'apps pull --code' to refresh)", s.NewerCount)
}

// humanSize formats a byte count as a short human-readable string.
func humanSize(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// writeStateLabel reports what writeIfNeeded did with a single file.
func writeStateLabel(res apps.WriteResult) string {
	if res.InSync {
		return "in sync"
	}
	return "written"
}

// writtenInSyncLabel renders e.g. "1 written, 2 in sync" or "all 3 in sync".
func writtenInSyncLabel(written, inSync int) string {
	switch {
	case written == 0 && inSync > 0:
		return fmt.Sprintf("all %d in sync", inSync)
	case written > 0 && inSync == 0:
		return fmt.Sprintf("%d written", written)
	default:
		return fmt.Sprintf("%d written, %d in sync", written, inSync)
	}
}

// inSyncDetail produces the trailing description for an app that had no
// changes, e.g. ", 3 env vars, 1 secret file, 1 override".
func inSyncDetail(a pulledAppEntry) string {
	var parts []string
	if a.EnvVars > 0 {
		parts = append(parts, plural(a.EnvVars, "env var", "env vars"))
	}
	if a.SecretEnvVars > 0 {
		parts = append(parts, plural(a.SecretEnvVars, "secret env var", "secret env vars"))
	}
	if a.SecretFilesTotal > 0 {
		parts = append(parts, plural(a.SecretFilesTotal, "secret file", "secret files"))
	}
	if a.OverridesTotal > 0 {
		parts = append(parts, plural(a.OverridesTotal, "override", "overrides"))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func displayTarget(t apps.AppSummary) string {
	if t.Name != "" && t.ID != "" {
		return fmt.Sprintf("%s (%s)", t.Name, t.ID)
	}
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

func emitJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// vcsRepoRelPath returns the repo-relative form of an absolute filesystem
// path, suitable for comparison against a server-stored `configPath`.
// Returns "" when we're not inside a git checkout, or when the path lives
// outside the repo root (Rel produces a `..` prefix in that case). Mirrors
// the shape of cmd/deploy_vcs.go:resolveVcsConfigPath, kept here as a
// small free function so apps_pull doesn't have to drag the deploy config
// loader along just to compute one path.
func vcsRepoRelPath(absPath string) string {
	if !git.IsRepo() {
		return ""
	}
	repoRoot, err := git.RepoRoot()
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// loadLocalManifest reads the manifest cache at ~/.runos/manifest.json.
// Used by the apps_pull services cascade and any other code path that
// needs read-only access to the manifest. Surfaces a clear error so the
// caller can fall back gracefully (cascade off, deploy proceeds, etc).
func loadLocalManifest(conductorURL string) (*manifest.Manifest, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home dir: %w", err)
	}
	configDir := filepath.Join(home, ".runos")
	loader := manifest.NewLoader(conductorURL, configDir)
	// Load() falls through to a remote fetch when the cache is missing
	// or stale; the apps_pull cascade needs a working manifest, and a
	// fresh CI runner won't have the cache pre-populated.
	m, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("load manifest (run 'runos manifest update'?): %w", err)
	}
	return m, nil
}

// cascadePulledServices walks the just-pulled app yaml's requires block
// and pulls a runos.service.<cid>.<sid>.yaml for every entry whose id is
// set and whose service yaml isn't already on disk in appDir.
//
// Errors per requires entry are surfaced as skip records so a single
// failed service pull doesn't abort the rest. Cascade is best-effort:
// the app yaml is already on disk, the cascade just brings the
// dependency yamls along.
func cascadePulledServices(exec *dynacmd.Executor, m *manifest.Manifest, appDir, cid, aid, appID string) []pullSkipEntry {
	var skips []pullSkipEntry
	yamlLeaf, err := apps.YAMLFilename(appDir, cid, appID)
	if err != nil {
		return []pullSkipEntry{{ID: appID, Reason: fmt.Sprintf("services cascade: locate yaml: %v", err)}}
	}
	localApp, err := apps.LoadLocalApp(filepath.Join(appDir, yamlLeaf))
	if err != nil {
		return []pullSkipEntry{{ID: appID, Reason: fmt.Sprintf("services cascade: read yaml: %v", err)}}
	}
	// V4: prefer a workspace scan from the git repo root over an
	// appDir-only check so projects that organise services in a sibling
	// directory (e.g. `infra/runos/apps/` + `infra/runos/services/`)
	// don't accumulate duplicate yamls next to the app yaml on every
	// pull. The fallback to appDir-only handles the no-git-checkout
	// case so the single-app-at-root cascade still works as before.
	// Both branches share services.ExistingServiceYamlPath.
	repoRoot, _ := git.RepoRoot()
	for alias, r := range localApp.Requires {
		if r.ID == "" {
			continue
		}
		// Header-based lookup so a renamed service yaml is recognised
		// as already-pulled, regardless of filename. If found anywhere
		// reachable, leave alone (the user's `runos services pull
		// <yaml>` is the way to refresh).
		if existing := services.ExistingServiceYamlPath(repoRoot, appDir, cid, r.ID); existing != "" {
			continue
		}
		pulled, err := services.Pull(exec, m, r.Type, cid, aid, r.ID)
		if err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     appID,
				Name:   alias,
				Reason: fmt.Sprintf("services cascade %s/%s: %v", r.Type, r.ID, err),
			})
			continue
		}
		dest := services.FilenameFor(appDir, cid, r.Type, r.ID)
		if err := services.Save(dest, pulled); err != nil {
			skips = append(skips, pullSkipEntry{
				ID:     appID,
				Name:   alias,
				Reason: fmt.Sprintf("services cascade %s/%s: save: %v", r.Type, r.ID, err),
			})
		}
	}
	return skips
}



