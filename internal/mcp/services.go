package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// staticServicesTools mirrors staticAppsTools for the services pull /
// diff / sync surface. Like the apps versions, these orchestrate local
// filesystem state alongside conductor calls so they live outside the
// manifest-driven dynamic tool set; the MCP path runs them by shelling
// out to the same runos binary.
//
// Filtering by category:
//   - read:  pull, diff (responses are file paths + diff text only)
//   - write: sync (mutates cluster state)
func staticServicesTools(category string) []Tool {
	var tools []Tool

	if category == "read" {
		tools = append(tools, Tool{
			Name: "services_pull",
			Description: `Pull a service's current config into a local runos.service.<cid>.<sid>.yaml.

BEFORE CALLING: If the user's request didn't specify where the file should land, ASK them.
The two intents to disambiguate are:
  - "into this directory" -> set out="." (or the cwd path).
  - "into a new subdirectory" -> set out=<dir>.
Default (no out) is cwd. The leaf name is always runos.service.<cid>.<sid>.yaml.

Modes (precedence, first match wins):
  1. yaml_file set:                    re-pull the named yaml in place.
  2. type set + service_id set:        first-time pull. Writes runos.service.<cid>.<sid>.yaml.

Drift gate: if the local file has diverged from the server, pull refuses without force=true.

The yaml schema is derived from the conductor manifest at runtime, so adding a new
service type or field on conductor side flows through here without a CLI change.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Defaults to the configured default cluster.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to an existing runos.service.<cid>.<sid>.yaml. Re-pulls in place; type/cid/id come from the file.",
					},
					"type": {
						Type:        "string",
						Description: "Service type (e.g. postgresql, valkey, mysql). Required for first-time pull when yaml_file is not set.",
					},
					"service_id": {
						Type:        "string",
						Description: "Service id (5-char identifier). Required for first-time pull when yaml_file is not set.",
					},
					"out": {
						Type:        "string",
						Description: "Output directory for first-time pull. Pass \".\" for cwd; omit to use cwd. Ignored when yaml_file is set.",
					},
					"force": {
						Type:        "boolean",
						Description: "Overwrite the local file even when it has diverged from server state.",
						Default:     false,
					},
				},
			},
		})

		tools = append(tools, Tool{
			Name: "services_diff",
			Description: `Compare a local runos.service.<cid>.<sid>.yaml against the cluster. Reports drift in JSON without writing anything.

Use this before services_sync to preview pushes, or as a CI gate (drift -> non-zero exit).

yaml_file is required when called via MCP.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Cross-checked against the yaml's own cid field.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to the runos.service.<cid>.<sid>.yaml that describes the local state.",
					},
				},
				Required: []string{"yaml_file"},
			},
		})
	}

	if category == "write" {
		tools = append(tools, Tool{
			Name: "services_sync",
			Description: `Push a local runos.service.<cid>.<sid>.yaml back to the cluster. Reverse of services_pull.

Two modes are picked from the yaml's id field:
  - id present: PATCH /services/<type>/<id>. Sends the local yaml's PATCHable fields.
                Conductor's per-type omit-equals-preserve / omit-equals-clear rules apply
                server-side; immutable-after-create fields surface as "refused".
  - id absent:  POST /services/<type>. Provisions a new service. The new id is
                written back to the yaml on success.

Pass dry_run=true to compute the plan without applying. The interactive confirmation
is auto-skipped when called via MCP.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"cid": {
						Type:        "string",
						Description: "Cluster ID in format 'xyz (Cluster Name)'. Cross-checked against the yaml's own cid field.",
					},
					"yaml_file": {
						Type:        "string",
						Description: "Path to the runos.service.<cid>.<sid>.yaml whose changes should be pushed.",
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

// isStaticServicesTool reports whether toolName is one of the static
// services pull/diff/sync tools (rather than a manifest-driven service
// command like services_postgresql_show).
func isStaticServicesTool(toolName string) bool {
	switch toolName {
	case "services_pull", "services_diff", "services_sync":
		return true
	}
	return false
}

// handleServicesCommand dispatches one of the static services_* tools to
// a runos subprocess. Mirrors handleAppsCommand: same 10-minute timeout,
// same stdout+stderr capture, same lockstep-with-CLI behaviour.
func (s *Server) handleServicesCommand(toolName string, args map[string]any) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	cmdArgs, err := buildServicesCommandArgs(toolName, args)
	if err != nil {
		return "", err
	}

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

// buildServicesCommandArgs translates an MCP tool call into a runos
// argv. Per-tool quirks:
//   - read tools (pull/diff) get --json so the LLM gets structured output.
//   - sync gets --yes (no stdin) and --redact-secrets (env values from
//     a service's flags/credentials would otherwise flow into LLM context).
func buildServicesCommandArgs(toolName string, args map[string]any) ([]string, error) {
	switch toolName {
	case "services_pull":
		return buildServicesPullArgs(args), nil
	case "services_diff":
		return buildServicesDiffArgs(args)
	case "services_sync":
		return buildServicesSyncArgs(args)
	}
	return nil, fmt.Errorf("unknown services tool: %s", toolName)
}

func buildServicesPullArgs(args map[string]any) []string {
	out := []string{"services", "pull", "--json"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	if t, ok := stringArg(args, "type"); ok {
		out = append(out, "--type", t)
	}
	if sid, ok := stringArg(args, "service_id"); ok {
		out = append(out, "--service-id", sid)
	}
	if dir, ok := stringArg(args, "out"); ok {
		out = append(out, "--out", dir)
	}
	if boolArg(args, "force") {
		out = append(out, "--force")
	}
	// `--` and the yaml positional must come last: a yaml path starting
	// with `-` can't be reinterpreted as a flag once after `--`.
	if yaml, ok := stringArg(args, "yaml_file"); ok {
		out = append(out, "--", yaml)
	}
	return out
}

func buildServicesDiffArgs(args map[string]any) ([]string, error) {
	yaml, ok := stringArg(args, "yaml_file")
	if !ok {
		return nil, fmt.Errorf("services_diff: yaml_file is required")
	}
	out := []string{"services", "diff", "--json"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	out = append(out, "--", yaml)
	return out, nil
}

func buildServicesSyncArgs(args map[string]any) ([]string, error) {
	yaml, ok := stringArg(args, "yaml_file")
	if !ok {
		return nil, fmt.Errorf("services_sync: yaml_file is required")
	}
	out := []string{"services", "sync", "--yes", "--redact-secrets"}
	if cid, ok := stringArg(args, "cid"); ok {
		out = append(out, "--cid", extractCID(cid))
	}
	if boolArg(args, "dry_run") {
		out = append(out, "--dry-run")
	}
	out = append(out, "--", yaml)
	return out, nil
}
