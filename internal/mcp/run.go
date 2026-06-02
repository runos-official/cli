package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// staticRunTools registers the `runos run` static MCP tool. Lives
// outside the manifest because the underlying CLI verb blocks
// streaming container output, exits with the container's real exit
// code, and accepts a free-form trailing command argv — a shape the
// manifest's one-HTTP-call dispatcher doesn't model.
//
// The verb only ever runs against a VCS-deploy app and refuses
// CLI-deploy apps at the client; agents that drive a CLI-deploy app
// must use the deploy/migration alternative for that app rather than
// retrying with different flags.
//
// MCP semantics: the tool blocks until the run reaches a terminal
// state (success / failure / timeout) or the 60-minute subprocess
// ceiling fires, whichever comes first, and returns the streamed
// progress + final exit code as a single text payload.
func staticRunTools(category string) []Tool {
	if category != "sensitive_write" {
		return nil
	}

	return []Tool{
		{
			Name: "run",
			Description: `Execute a one-off command/script in the cluster from a VCS app's image at a specific commit SHA. Sibling to deploy: deploy is build+rollout, run is build+execute.

WHEN TO USE: pre-rollout tasks (DB migrations, seeds, backfills, one-off maintenance). The canonical CI ordering is run THEN deploy: ` + "`runos run ... scripts/release.sh`" + ` then ` + "`runos deploy ... --sha <same-sha>`" + `. Both verbs are keyed on the SHA, so the second one reuses the image already in Harbor and only does the rollout.

RECOMMENDED SHAPE: pass a script path baked into the image (e.g. scripts/release.sh) as the single command entry. That keeps multi-step logic (migrate, backfill, conditional seed) in version-controlled code with real error handling, not in CI YAML. A bare argv (e.g. ["alembic","upgrade","head"]) is allowed for the trivial case.

VCS-ONLY: only deployType=vcs apps are supported. Passing a CLI-deploy app id is refused at the client with a clear error. There is no SHA-keyed build-on-demand path for CLI-deploy apps; that scope is intentionally out for this verb.

EXIT CODE: the CLI exits with the container's real exit code. A non-zero command exit propagates as a non-zero CLI exit so a CI step that gates on the run will fail correctly. A timeout kill also exits non-zero.

ENV: the Job pod runs with the app's existing ConfigMap + Secret injected via envFrom (same env the app pod runs with). When this is the first-ever run before any deploy, the conductor still ensures the app's namespace env/secrets exist before dispatching, so the run gets the app's full env.

CONCURRENCY: the conductor rejects a second run while one is already in flight for the same app (409). Do not retry on 409 in a loop — wait for the in-flight run to finish or surface the conflict to the user.

OUTPUT: the streamed work-item log + final status is returned as a single text payload. The audit record (command, image SHA, exit code, timestamp, actor) persists server-side; for now there is no after-the-fact retrieval verb, so the live output is the only view.`,
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"app": {
						Type:        "string",
						Description: "VCS-deploy app ID (5-char identifier) to run against. Required for the CI-shape invocation MCP exercises (no laptop yaml is loaded under MCP).",
					},
					"sha": {
						Type:        "string",
						Description: "Commit SHA (40-char lowercase hex) to run at. Required; the conductor builds image@sha on demand if missing and reuses it from Harbor otherwise.",
					},
					"cid": {
						Type:        "string",
						Description: "Cluster ID (the bare id, e.g. 'mycluster2'). REQUIRED if no default cluster is set. Get from user or use clusters_list.",
					},
					"command": {
						Type:        "array",
						Description: "The argv to execute inside the container. The first entry should be the script path (e.g. \"scripts/release.sh\") or the command name (e.g. \"alembic\"); subsequent entries are its arguments. Replaces the image's default entrypoint, so the script must be present at the given path inside the image at <sha>.",
						Items:       &Property{Type: "string"},
					},
					"timeout": {
						Type:        "string",
						Description: "Optional Go duration ceiling (e.g. \"30m\", \"1h\"). The conductor enforces 7200s (2h) as a hard cap; absent uses the server default (1800s / 30m). On timeout the Job is killed and the CLI exits non-zero.",
					},
				},
				Required: []string{"app", "sha", "command"},
			},
		},
	}
}

// isStaticRunTool reports whether toolName is the static `run` tool.
// Kept symmetric with the existing isStatic*Tool predicates so the
// dispatch branch in CallTool is grep-friendly.
func isStaticRunTool(toolName string) bool {
	return toolName == "run"
}

// handleRun dispatches the `run` MCP tool to a runos subprocess. The
// 60-minute ceiling is the MCP tool's outer wall-clock budget; the
// conductor's own per-run timeout (default 30m, max 2h) is what
// actually bounds the in-cluster Job. We give the subprocess room past
// the conductor's hard cap so a near-cap run still finishes inside
// the subprocess window.
func (s *Server) handleRun(args map[string]any) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	cmdArgs, err := buildRunArgs(args)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
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
		// Subprocess exit-code carries the container's real exit code
		// (see cmd/root.go Execute()), so an *exec.ExitError isn't a
		// CLI bug — it's the contract. Surface the streamed output
		// verbatim and tag the error with the exit code so the LLM
		// caller sees the actual code.
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			if output == "" {
				return "", fmt.Errorf("run exited non-zero (code %d)", code)
			}
			return "", fmt.Errorf("run exited non-zero (code %d):\n%s", code, output)
		}
		if output == "" {
			return "", fmt.Errorf("run failed: %w", runErr)
		}
		return "", fmt.Errorf("run failed: %s", output)
	}

	return output, nil
}

// buildRunArgs translates an MCP run tool call into a runos argv. The
// trailing positional argv (the script/command) goes after every flag,
// separated by `--` so an entry starting with `-` doesn't get parsed
// as a CLI flag.
func buildRunArgs(args map[string]any) ([]string, error) {
	cmdArgs := []string{"run"}

	if v, ok := stringArg(args, "app"); ok && v != "" {
		cmdArgs = append(cmdArgs, "--app", v)
	}
	if v, ok := stringArg(args, "sha"); ok && v != "" {
		cmdArgs = append(cmdArgs, "--sha", v)
	}
	if v, ok := stringArg(args, "cid"); ok && v != "" {
		cmdArgs = append(cmdArgs, "--cid", v)
	}
	if v, ok := stringArg(args, "timeout"); ok && v != "" {
		cmdArgs = append(cmdArgs, "--timeout", v)
	}
	// MCP callers always run headless; -y skips the confirmation prompt
	// the CLI auto-skips on non-TTY anyway, but keeping it explicit
	// avoids surprising drift if the prompt's TTY check ever changes.
	cmdArgs = append(cmdArgs, "-y")

	command, err := stringArrayArg(args, "command")
	if err != nil {
		return nil, err
	}
	if len(command) == 0 {
		return nil, fmt.Errorf("run: command (non-empty argv) is required")
	}
	for i, c := range command {
		if c == "" {
			return nil, fmt.Errorf("run: command[%d] is empty", i)
		}
	}
	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, command...)
	return cmdArgs, nil
}

// stringArrayArg pulls a []string out of the loosely-typed MCP args
// map. Accepts either a native []string (rare) or the JSON-decoded
// []any with string elements (the common case from any MCP client
// going through json.Unmarshal).
func stringArrayArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for i, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string, got %T", key, i, e)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings, got %T (try " + strconv.Quote("[]string") + ")", key, raw)
	}
}
