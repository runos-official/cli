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
