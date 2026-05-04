package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// staticAppsTools returns the MCP tool descriptors for the static apps
// subcommands (pull, diff, sync, list-previous-uploads). These are not
// part of the manifest because they orchestrate local filesystem state
// alongside API calls, the MCP path runs them by shelling out to the
// runos binary, the same way deploy does.
//
// The returned slice is filtered to the categories appropriate for each
// command:
//   - read: pull, diff, list-previous-uploads (responses carry metadata
//     only; secrets/env content go to disk, not into the MCP response)
//   - write: sync (modifies live cluster state)
func staticAppsTools(category string) []Tool {
	var tools []Tool

	if category == "read" {
		tools = append(tools, Tool{
			Name: "apps_pull",
			Description: `Pull running app config (and optionally source code) into a local directory.

BEFORE CALLING: If the user's request didn't clearly specify where files should land, ASK them.
The two intents to disambiguate are:
  - "into this directory" -> set out="." (or the cwd path). Files land flat in cwd.
  - "into a new subdirectory" -> omit out. A runos.<cid>.<id>/ subdir is created.
Don't guess: pulling into the wrong place surprises users (especially with code, which can clobber files).

Modes (precedence, first match wins):
  1. yaml_file set:                target = the yaml's directory.
  2. all=true:                     bulk-pull every app; each gets its own runos.<cid>.<id>/ subdir.
  3. app_id set + out set:         single app, flat into the named directory.
  4. app_id set, out unset:        single app, into ./runos.<cid>.<id>/ (matches bulk default).
  5. None of the above:            scan cwd for a runos*.yaml; unique match becomes yaml_file. Errors on 0 or 2+ matches.

Each app's directory contains runos.yaml, .env, .secret-files/, overrides/, and optionally
the extracted source. Pass code=true to also pull the latest CLI-deploy archive, or
code_version=<cliUploadID> for a specific older version (discover via apps_list_previous_uploads).

Drift gate: if local files have diverged from the server, pull refuses without force=true.
all and code/code_version are mutually exclusive (bulk runs do not pull code).
all and yaml_file/app_id are mutually exclusive.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Defaults to the configured default cluster.",
					},
					"all": {
						Type:        "boolean",
						Description: "Pull every app in the cluster. Required for bulk mode. Mutually exclusive with yaml_file, app_id, code, code_version.",
						Default:     false,
					},
					"app_id": {
						Type:        "string",
						Description: "App ID to pull (alternative to yaml_file). With out=<dir>, files land flat in that dir; without out, they land in ./runos.<cid>.<id>/.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to an existing pulled runos.yaml. Target directory defaults to the yaml's parent dir; pass out to override.",
					},
					"out": {
						Type:        "string",
						Description: "Output directory. With all: parent for per-app subdirs. With app_id or yaml_file: exact target (flat into this dir). Pass \".\" to land directly in the current directory; omit entirely to create a new runos.<cid>.<id>/ subdirectory.",
					},
					"force": {
						Type:        "boolean",
						Description: "Overwrite local files even when they have diverged from server state. Requires a single-app target.",
						Default:     false,
					},
					"code": {
						Type:        "boolean",
						Description: "Also pull the most recent CLI-deploy source archive. Single-app only.",
						Default:     false,
					},
					"code_version": {
						Type:        "string",
						Description: "Pull a specific archive by cliUploadID (rollback flow). Implies code=true. Use apps_list_previous_uploads to discover IDs.",
					},
				},
			},
		})

		tools = append(tools, Tool{
			Name: "apps_diff",
			Description: `Compare a local pulled runos.yaml (and the env/secret files/overrides it references) against the current cluster state. Reports drift in JSON without writing anything.

Returns drift details for yaml, env, secret files, and overrides. Use this before apps_sync to preview pushes, or as a CI gate (drift -> non-zero exit).

yaml_file is required when called via MCP (the MCP subprocess can't reliably auto-detect from a cwd you can't see).`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Cross-checked against the yaml's own cid field.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to the pulled runos.yaml that describes the local state.",
					},
				},
				Required: []string{"yaml_file"},
			},
		})

		tools = append(tools, Tool{
			Name: "apps_list_previous_uploads",
			Description: `List previously CLI-uploaded source archives for an app. Each entry has a cliUploadID you can pass to apps_pull as code_version to restore that exact source locally; running runos deploy from the per-app directory then redeploys it.

Apps deployed via git/CI (not the runos deploy command) will return an empty list, they have no CLI archives.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Cross-checked against the yaml's own cid field.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to the pulled runos.yaml that identifies the app.",
					},
				},
				Required: []string{"yaml_file"},
			},
		})
	}

	if category == "write" {
		tools = append(tools, Tool{
			Name: "apps_sync",
			Description: `Push local app config (yaml, env, secret files, overrides) to the cluster. Reverse of apps_pull.

Sync runs as plan -> apply: it computes deltas against current server state and pushes them. Pass dry_run=true to compute the plan without applying. The interactive confirmation prompt is auto-skipped when called via MCP.

What sync can push:
  - yaml fields (replicas, ports, healthCheck, resources)
  - env vars (replace-all)
  - secret files (add/update/remove)
  - overrides (add/update/delete)

What sync cannot push:
  - VCS / integration fields (deployType, repo, branch). Use the console.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Cross-checked against the yaml's own cid field.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to the pulled runos.yaml whose changes should be pushed.",
					},
					"dry_run": {
						Type:        "boolean",
						Description: "Compute the sync plan but don't apply it.",
						Default:     false,
					},
				},
				Required: []string{"yaml_file"},
			},
		})
	}

	return tools
}

// isStaticAppsTool reports whether toolName is one of the apps subcommands
// handled by handleAppsCommand (rather than the manifest-driven path).
func isStaticAppsTool(toolName string) bool {
	switch toolName {
	case "apps_pull", "apps_diff", "apps_sync", "apps_list_previous_uploads":
		return true
	}
	return false
}

// handleAppsCommand dispatches one of the static apps_* tools to a runos
// subprocess and returns the captured output. The runos binary is the
// same executable currently running the MCP server, so behaviour stays
// in lockstep with the CLI.
func (s *Server) handleAppsCommand(toolName string, args map[string]any) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	cmdArgs, err := buildAppsCommandArgs(toolName, args)
	if err != nil {
		return "", err
	}

	// Sync can take longer than read paths (it pushes deltas, possibly
	// triggering rollouts on the server). Pull with --code can also be
	// slow for large archives. Give every apps subcommand the same 10
	// minute ceiling deploy uses.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if runErr != nil {
		if output == "" {
			return "", fmt.Errorf("%s failed: %w", toolName, runErr)
		}
		return "", fmt.Errorf("%s failed: %s", toolName, output)
	}

	return output, nil
}

// buildAppsCommandArgs translates an MCP tool call into a runos argv.
// Per-tool quirks:
//   - read tools (pull/diff/list-previous-uploads) get --json so the AI
//     receives structured output.
//   - sync gets --yes to skip the interactive confirmation (stdin isn't
//     attached when running under MCP).
func buildAppsCommandArgs(toolName string, args map[string]any) ([]string, error) {
	switch toolName {
	case "apps_pull":
		return buildAppsPullArgs(args), nil
	case "apps_diff":
		return buildAppsDiffArgs(args)
	case "apps_sync":
		return buildAppsSyncArgs(args)
	case "apps_list_previous_uploads":
		return buildAppsListPreviousUploadsArgs(args)
	}
	return nil, fmt.Errorf("unknown apps tool: %s", toolName)
}

func buildAppsPullArgs(args map[string]any) []string {
	out := []string{"apps", "pull", "--json"}
	if boolArg(args, "all") {
		out = append(out, "--all")
	}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	if appID, ok := stringArg(args, "app_id"); ok {
		out = append(out, "--app-id", appID)
	}
	if dir, ok := stringArg(args, "out"); ok {
		out = append(out, "--out", dir)
	}
	if boolArg(args, "force") {
		out = append(out, "--force")
	}
	if codeVer, ok := stringArg(args, "code_version"); ok {
		out = append(out, "--code-version", codeVer)
	} else if boolArg(args, "code") {
		out = append(out, "--code")
	}
	// Positional last, after the `--` end-of-flags marker so a yaml
	// path that happens to start with `-` (or any flag-shaped value
	// the LLM might pass) can't be reinterpreted as a flag.
	if yaml, ok := stringArg(args, "yaml_file"); ok {
		out = append(out, "--", yaml)
	}
	return out
}

func buildAppsDiffArgs(args map[string]any) ([]string, error) {
	yaml, ok := stringArg(args, "yaml_file")
	if !ok {
		return nil, fmt.Errorf("apps_diff: yaml_file is required")
	}
	// --redact-secrets is mandatory under MCP: env values would
	// otherwise flow into LLM context via the env section's UnifiedDiff.
	// The CLI keeps showing values for human users; only the MCP path
	// redacts.
	out := []string{"apps", "diff", "--json", "--redact-secrets"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	// `--` and the yaml positional must come last: anything appended
	// after `--` is treated as a positional by Cobra, not a flag.
	out = append(out, "--", yaml)
	return out, nil
}

func buildAppsSyncArgs(args map[string]any) ([]string, error) {
	yaml, ok := stringArg(args, "yaml_file")
	if !ok {
		return nil, fmt.Errorf("apps_sync: yaml_file is required")
	}
	// --yes is mandatory under MCP; the subprocess has no stdin attached
	// for confirmation, and we explicitly want non-interactive behaviour.
	// --redact-secrets replaces env values in plan output with <redacted>
	// so they don't flow into LLM context.
	out := []string{"apps", "sync", "--yes", "--redact-secrets"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	if boolArg(args, "dry_run") {
		out = append(out, "--dry-run")
	}
	// `--` and the yaml positional must come last: anything appended
	// after `--` is treated as a positional by Cobra, not a flag.
	out = append(out, "--", yaml)
	return out, nil
}

// buildDeployArgs translates an MCP deploy tool call into a runos argv.
// Always passes --follow so the AI sees the build/deploy job to
// completion. Forwards cid (after stripping the "(Cluster Name)" suffix),
// yaml_file (as the CLI's --config flag, required for multi-yaml dirs),
// and force when set.
func buildDeployArgs(args map[string]any) []string {
	out := []string{"deploy", "--follow"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	if yamlFile, ok := stringArg(args, "yaml_file"); ok {
		out = append(out, "--config", yamlFile)
	}
	if boolArg(args, "force") {
		out = append(out, "--force")
	}
	return out
}

func buildAppsListPreviousUploadsArgs(args map[string]any) ([]string, error) {
	yaml, ok := stringArg(args, "yaml_file")
	if !ok {
		return nil, fmt.Errorf("apps_list_previous_uploads: yaml_file is required")
	}
	out := []string{"apps", "list-previous-uploads", "--json"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	// `--` separates the positional yaml from any preceding flag value
	// so a yaml path that starts with `-` can't be misread as a flag.
	out = append(out, "--", yaml)
	return out, nil
}

func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

func boolArg(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
