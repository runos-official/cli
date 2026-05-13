package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/runos-official/cli/internal/deploy"
	"github.com/runos-official/cli/internal/git"
	"github.com/runos-official/cli/internal/jobs"
)

// vcsDeployJSONResponse is the on-success stdout shape for
// `runos deploy --json` on a VCS-deploy app. Mirrors the CLI-deploy
// branch's response (which marshals `deploy.PrepareCliDeploymentResponse`)
// well enough that CI gates can read `.jobId` / `.appId` across both
// deployTypes without branching. publicURL is populated only when
// `--follow` succeeded and the conductor returned a `RUNOS_PUBLIC*`
// network access entry. Regression target: I24b-A.
type vcsDeployJSONResponse struct {
	JobID      string `json:"jobId"`
	AppID      string `json:"appId"`
	SHA        string `json:"sha"`
	ConfigPath string `json:"configPath,omitempty"`
	PublicURL  string `json:"publicUrl,omitempty"`
}

// vcsDeployStreams returns the (stdout, human) writer pair runDeployVCS
// uses to keep the JSON envelope on stdout and route the human-readable
// banner/progress to stderr when `--json` is set. Extracted as a pure
// helper so the routing contract has a unit-test pin (the runDeployVCS
// integration path requires a live deploy.Service and can't be exercised
// without a real cluster). Regression target: I24b-A.
func vcsDeployStreams(jsonOutput bool) (stdout, human io.Writer) {
	stdout = os.Stdout
	human = os.Stdout
	if jsonOutput {
		human = os.Stderr
	}
	return stdout, human
}

// printVCSDeployBanner writes the post-API-2xx VCS-deploy preamble
// ("Deploying app X @ Y..." + configPath line) to `human`. Callers
// pass the result of vcsDeployStreams; under `--json` `human` is
// os.Stderr so stdout stays parseable. Regression target: I24b-A.
func printVCSDeployBanner(human io.Writer, appID, sha, configPath string) {
	fmt.Fprintf(human, "Deploying app %s @ %s...\n", appID, shortSHA(sha))
	if configPath != "" {
		fmt.Fprintf(human, "  configPath: %s\n", configPath)
	} else {
		fmt.Fprintln(human, "  configPath: (using AppDocument default)")
	}
}

// resolveVcsConfigPath returns the repo-relative path of the local yaml
// to send on a VCS deploy request, so the cluster agent reads the right
// file from the committed tree at <sha>. Three sources, priority order:
//
//   1. Explicit `configPath:` field in the yaml — escape hatch for non-
//      standard layouts (e.g. yaml lives outside the repo, or the user
//      vendored the file under a different path than where it lives in
//      the source repo).
//   2. Auto-derived from the yaml's filesystem path relative to the git
//      repo root. Common case: the yaml is committed inside the repo and
//      the user's checkout has it at its repo-canonical location. The
//      user doesn't have to set anything.
//   3. Empty string — couldn't determine. Conductor falls back to whatever
//      the AppDocument has stored (default `runos.yaml` for fresh apps).
//
// Always emits forward-slash paths (Windows backslashes get converted)
// since the conductor and cluster agent treat configPath as a posix-style
// repo path regardless of the developer's OS.
func resolveVcsConfigPath(cfg *deploy.DeployConfig, configFileAbs string) string {
	if cfg.ConfigPath != "" {
		return filepath.ToSlash(cfg.ConfigPath)
	}
	if !git.IsRepo() {
		return ""
	}
	repoRoot, err := git.RepoRoot()
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(repoRoot, configFileAbs)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(rel, "..") {
		// Yaml lives outside the repo — auto-derivation would produce a
		// path that escapes the workdir on the cluster agent and fail.
		return ""
	}
	return filepath.ToSlash(rel)
}

// runDeployVCS triggers a VCS deploy via POST /apps/:id/deploy. Conductor
// owns source delivery (it pulls from the linked GitHub/GitLab integration);
// the CLI's job is to resolve the SHA, refuse dirty trees, and follow job
// progress.
//
// configPath is the repo-relative path to the runos.yaml that the cluster
// agent should read on this deploy. The caller in runDeploy auto-derives
// it from the loaded yaml's position relative to the git repo root (with
// a yaml-level `configPath:` field as an explicit override). Empty when
// running in CI mode with --app and no yaml on disk; the conductor then
// falls back to whatever the AppDocument has stored.
//
// Sibling to the CLI-deploy code in deploy.go: the two share the verb name
// but no downstream logic. runDeployVCS is dispatched from runDeploy when
// the resolved app's deployType is 'vcs'.
func runDeployVCS(svc *deploy.Service, appID, sha, configPath string, allowDirty, follow, skipPrompt, jsonOutput bool) error {
	// Under --json, every human-readable line (preamble, configPath note,
	// "Deployment initiated", follow progress, network-access tail) goes
	// to stderr so stdout stays pure JSON for `jq` / MCP consumers. The
	// CLI-deploy branch (runDeploy in cmd/deploy.go) does the same via
	// its `progress` shim; this mirrors that contract for VCS deploys so
	// `runos deploy --json` returns a parseable envelope on both
	// deployTypes. Regression target: I24b-A.
	stdout, human := vcsDeployStreams(jsonOutput)

	// Resolve the SHA. Explicit --sha wins; otherwise default to HEAD when
	// we're inside a git repo. CI checkouts always produce a repo so this
	// works without special-casing CI vs laptop.
	shaProvided := sha != ""
	if sha == "" {
		if !git.IsRepo() {
			return fmt.Errorf("--sha is required when not running from inside a git checkout")
		}
		head, err := git.GetHEAD()
		if err != nil {
			return fmt.Errorf("could not resolve HEAD: %w", err)
		}
		sha = head
	}

	// I25-Y: the dirty-tree gate is only meaningful when sha was
	// auto-derived from HEAD. When --sha is explicit (canonical case:
	// `runos deploy --app X --sha Y --cid Z`), the build runs against
	// the committed source at <sha> on the git host and the local
	// working tree's state is by definition orthogonal. Firing the gate
	// in that case is noise; skip it.
	if !shaProvided && git.IsRepo() {
		dirty, err := git.IsDirty()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not check git status: %v\n", err)
		} else if dirty {
			if !allowDirty {
				return fmt.Errorf("working tree is dirty; commit changes or pass --allow-dirty to deploy at HEAD anyway")
			}
			fmt.Fprintln(os.Stderr, "Warning: deploying with --allow-dirty; uncommitted local changes will NOT be in the build")
		}
	}

	inGitHubActions := os.Getenv("GITHUB_ACTIONS") == "true"

	// Confirmation gate: same shape as the CLI-deploy branch. Auto-skipped
	// in CI (non-TTY stdin) so workflows that pass --app + --sha don't need
	// any new flag; --yes is the explicit opt-out for TTY users.
	if err := confirmDeploy(buildVCSDeploySummary(appID, svc.CID(), svc.AID(), sha, configPath), skipPrompt); err != nil {
		return err
	}

	// I24-I: defer the "Deploying ..." banner so a submit-time API
	// rejection (malformed sha, missing app, etc.) isn't preceded by a
	// banner that misleadingly suggests the deploy is in progress. Banner
	// now prints AFTER the API 2xx. (Under --json it routes to stderr
	// regardless so stdout stays pure JSON.)
	resp, err := svc.DeployVCS(appID, sha, configPath)
	if err != nil {
		if inGitHubActions {
			fmt.Printf("::error::Failed to trigger deploy: %v\n", err)
		}
		return fmt.Errorf("failed to trigger VCS deploy: %w", err)
	}

	printVCSDeployBanner(human, appID, sha, configPath)

	if !follow {
		if jsonOutput {
			envelope := vcsDeployJSONResponse{
				JobID: resp.JobID, AppID: appID, SHA: sha, ConfigPath: configPath,
			}
			return writeJSON(stdout, envelope)
		}
		fmt.Fprintf(human, "\nDeployment initiated:\n")
		fmt.Fprintf(human, "  Job ID: %s\n", resp.JobID)
		fmt.Fprintf(human, "  App ID: %s\n", appID)
		fmt.Fprintf(human, "  SHA:    %s\n", sha)
		return nil
	}

	// Wrap the streamed job progress in a GitHub Actions log group so the
	// (verbose) build output collapses by default in the run UI. Outside CI
	// the markers are inert lines that look fine in a terminal.
	if inGitHubActions {
		fmt.Fprintln(human, "\n::group::Deploy progress")
	} else {
		fmt.Fprintln(human, "\nFollowing job progress...")
	}
	jobErr := jobs.FollowJobToWriter(resp.JobID, human)
	if inGitHubActions {
		fmt.Fprintln(human, "::endgroup::")
	}
	if jobErr != nil {
		if inGitHubActions {
			fmt.Printf("::error::Deployment failed: %v\n", jobErr)
		}
		return fmt.Errorf("deployment failed: %w", jobErr)
	}
	fmt.Fprintln(human, "\nDeployment completed successfully!")

	// Network-access tail (mirror the CLI-deploy success path). The public
	// URL also goes into a `::notice::` so it appears in the GitHub Actions
	// run summary panel, not just buried in the log.
	publicURL := ""
	networkAccess, err := svc.GetNetworkAccess(appID)
	if err == nil {
		for _, access := range networkAccess {
			if strings.HasPrefix(access.Type, "RUNOS_PUBLIC") {
				publicURL = access.Link
				fmt.Fprintf(human, "\nApp available at: %s\n", access.Link)
				if inGitHubActions {
					fmt.Printf("::notice::App available at %s\n", access.Link)
				}
				break
			}
		}
	}

	if jsonOutput {
		envelope := vcsDeployJSONResponse{
			JobID: resp.JobID, AppID: appID, SHA: sha, ConfigPath: configPath, PublicURL: publicURL,
		}
		return writeJSON(stdout, envelope)
	}
	return nil
}

// writeJSON marshals v as pretty JSON and writes it to w with a trailing
// newline. Used by runDeployVCS for the --json success-path envelope so
// CI gates parsing `.jobId` get a clean parseable response.
func writeJSON(w io.Writer, v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}
	if _, err := fmt.Fprintln(w, string(out)); err != nil {
		return err
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
