package mcp

import (
	"fmt"
	"strings"
)

// Switching an account module on or off changes the TOOL LIST, not just
// the account (FPL31 D15).
//
// A module owns a whole capability across every surface, so conductor
// leaves a disabled module's commands out of the account-scoped manifest.
// An agent that calls account_modules_enable therefore expects the VM
// tools to be there on its next turn, and without a refresh they are not:
// this server loaded its command list at startup and has no reason to
// look again for another 30 seconds.
//
// The refresh is the mechanism manifest_update already uses, and it obeys
// the same rule: announce ONLY a list that really changed. A toggle that
// adds no tool to THIS server's category must emit nothing, or a client
// re-reads hundreds of tool definitions to find nothing new.
//
// The other three MCP server processes hold their own copies and pick the
// change up on their next tools/list version re-check. Only the server
// that ran the toggle refreshes itself directly.

// moduleEnableToolName and moduleDisableToolName are the two manifest
// commands that change the enabled module set. Matched by TOOL name,
// which is the same whether conductor spells the manifest path with a
// {key} placeholder or with a positional field, because the placeholder
// is stripped before the name is built.
const (
	moduleEnableToolName  = "account_modules_enable"
	moduleDisableToolName = "account_modules_disable"
)

// isModuleToggleTool reports whether toolName switches a module on or off.
func isModuleToggleTool(toolName string) bool {
	return toolName == moduleEnableToolName || toolName == moduleDisableToolName
}

// handleModuleToggle runs the toggle, then refreshes this server's command
// list when the toggle succeeded.
//
// Refreshed here rather than left to the 30 s tools/list re-check, because
// the agent that ran the toggle wants the tools on its next turn.
func (s *Server) handleModuleToggle(toolName string, args map[string]any) (string, error) {
	result, err := s.executor.Execute(toolName, args)
	if err != nil {
		return "", err
	}
	result, changed := s.refreshAfterModuleToggle(result)
	if !changed {
		return result, nil
	}
	s.sendNotification("notifications/tools/list_changed")
	return moduleToggleNote(result), nil
}

// refreshAfterModuleToggle refetches the command list after a toggle that
// succeeded, and reports whether the tool list actually changed.
//
// BEST EFFORT, ALWAYS. The toggle itself already succeeded on the
// account; a refetch that fails must not turn that into an error the
// agent reads as "the module was not switched on". It appends one
// sentence naming manifest_update instead, so the agent has the one call
// that fixes it.
func (s *Server) refreshAfterModuleToggle(result string) (string, bool) {
	if s.reloader == nil {
		return result, false
	}
	before, after, err := s.reloadManifest()
	if err != nil {
		return result + fmt.Sprintf(
			"\n\nNOTE: the module was changed, but this server could not refresh its command list (%v). "+
				"Call %s to pick up the tools this change added or removed.", err, manifestUpdateToolName), false
	}
	return result, before != after
}

// moduleToggleNote is appended to a successful toggle whose refresh DID
// change the list, so the agent knows the tools it just gained are usable
// on this turn rather than after a reconnect.
func moduleToggleNote(result string) string {
	if strings.TrimSpace(result) == "" {
		return "The module was changed. This server's tool list has changed with it; re-read it."
	}
	return result + "\n\nThis server's tool list has changed with it; re-read it."
}
