package mcp

import (
	"fmt"

	"github.com/runos-official/cli/internal/manifest"
)

// confirmArgName is the tool argument through which an MCP caller states
// that a destructive verb is meant. It mirrors the CLI's `--yes`.
const confirmArgName = "confirm"

// gateTools are the manifest commands every MCP server lists, whatever
// its category. The bootstrap gate runs on all of them (B16), and a gate
// nobody can satisfy is a lockout, so the tool that satisfies it and the
// topic tools its instructions point at have to be reachable everywhere.
var gateTools = map[string]bool{
	"mcp/bootstrap":     true,
	"mcp/topics/search": true,
	"mcp/topics/show":   true,
}

// isGateTool reports whether a manifest command path is one of the
// bootstrap/topic tools that every server lists.
func isGateTool(command string) bool {
	return gateTools[command]
}

// bootstrapToolName is the tool that opens the bootstrap gate.
const bootstrapToolName = "mcp_bootstrap"

// isGateExemptTool reports whether a TOOL NAME runs whatever the gates
// say.
//
// Two tools qualify. mcp_bootstrap is the one that opens the gate.
// manifest_update refreshes this server's command list, needs no
// instructions to be safe, and is the only recovery from a stale list;
// keeping it behind the gate meant a server that could not bootstrap
// could not be repaired either (review 2 item 2).
func isGateExemptTool(toolName string) bool {
	return toolName == bootstrapToolName || toolName == manifestUpdateToolName
}

// bootstrapGateWarning is prepended to a tool result that ran with the
// bootstrap gate open only because bootstrap itself failed.
//
// A refusal is right for a caller that never tried. It is wrong for one
// whose attempt failed on an expired token or an unreachable conductor,
// because then no call the caller can make opens the gate and the whole
// server is dead. The work goes through and the answer says the
// instructions were never read. Regression target: review 2 item 2.
func bootstrapGateWarning(bootstrapErr string) string {
	warning := "[runos-mcp warning] mcp_bootstrap failed on this server, so its instructions were never read and this call ran without them"
	if bootstrapErr != "" {
		warning += ". The bootstrap error was: " + bootstrapErr
	}
	return warning + ". Fix that (an expired sign-in needs `runos login`), then call mcp_bootstrap again.\n\n"
}

// bootstrapRequired reports whether a server category refuses tool calls
// until mcp_bootstrap has been called.
//
// Every category except sensitive_read, which exists to hand a credential
// to a caller that already knows what it wants. Pre-fix only `read` was
// gated, so the two servers that CHANGE things were the ones with no gate
// (goal 21 B16).
func bootstrapRequired(category string) bool {
	switch category {
	case "read", "write", "sensitive_write":
		return true
	}
	return false
}

// withConfirmNotice appends the confirm requirement to a destructive
// tool's description.
//
// The schema already marks `confirm` required, but a strict-validating
// client is not the reader that matters here: the model composes the call
// from the description, and the one it read said nothing about confirm,
// so the first attempt was always refused (review 2 item 6).
func withConfirmNotice(description string) string {
	notice := "DESTRUCTIVE: this cannot be undone. The call is refused unless you pass confirm=true, and you may only pass it after the user has agreed to this exact target."
	if description == "" {
		return notice
	}
	return description + "\n\n" + notice
}

// requireOnce appends name to a JSON Schema `required` list unless it is
// already there.
//
// The injected `cid` and `confirm` arguments were appended without
// looking, so any command that also declares them as manifest fields
// shipped `"required":["cid","cid"]`. 35 tools carried the duplicate, and
// a strict validator rejects the schema outright (review 2 item 3).
func requireOnce(required []string, name string) []string {
	for _, existing := range required {
		if existing == name {
			return required
		}
	}
	return append(required, name)
}

// refuseUnconfirmedDestructive refuses a destructive tool call that did
// not state confirm=true.
//
// The CLI refuses a destructive verb outright when no TTY can answer its
// prompt, which is every CI, MCP and --json caller. MCP went straight
// through, so the surface an LLM drives was the unguarded one and a
// mistyped id was unrecoverable there and only there. Regression target:
// goal 19 A14.
func refuseUnconfirmedDestructive(cmdDef manifest.Command, args map[string]any, isDestructive func(manifest.Command) bool) error {
	if !isDestructive(cmdDef) {
		return nil
	}
	if confirmed, _ := args[confirmArgName].(bool); confirmed {
		return nil
	}
	return fmt.Errorf("%s is destructive and cannot be undone. Re-send this call with confirm=true, and only after the user has agreed to this exact target", cmdDef.Command)
}
