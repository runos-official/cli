package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// The guard answers one question for Cursor: may this MCP call go ahead?
//
// It keys on the MCP server name and never on a tool list. RunOS moves a tool
// between servers when its risk changes, so a tool list goes stale where a
// server name does not.
//
// Three outcomes, and the last one is the part that matters:
//
//	a RunOS server that carries risk   -> ask
//	any other named server             -> allow, because the hook fires for
//	                                      every MCP server in the project and
//	                                      must not block somebody else's
//	no server name the guard can read  -> ask, because a guard that cannot
//	                                      read its own payload has to be loud
//
// That last rule is deliberate. Cursor documents the field as mcp_server_name.
// If a future Cursor renames it, the user is asked about every call rather than
// silently allowed through, and the noise is the bug report.
var mcpCursorGuardCmd = &cobra.Command{
	Use:   "cursor-guard",
	Short: "Decide one Cursor MCP call from the payload on stdin",
	Long: `Read a Cursor beforeMCPExecution payload on stdin and print the permission decision.

Called by .cursor/hooks/runos-guard.sh, which 'runos mcp configure cursor' writes.
Not meant to be run by hand.`,
	Hidden: true,
	// The root command's PersistentPreRunE bootstraps the config from the CDN
	// and fetches the manifest on a first run. Cursor runs this hook before
	// every MCP call and times it out, so the guard must not reach the network,
	// and it must not refuse over an auth env var it never reads. Cobra runs
	// only the closest PersistentPreRunE in the chain, so declaring an empty
	// one here replaces root's for this command alone.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
	RunE:              runMCPCursorGuard,
}

// runMCPCursorGuard always prints a decision and always exits 0. Cursor takes
// the decision from stdout, so a run that prints nothing is a run that decides
// nothing.
func runMCPCursorGuard(cmd *cobra.Command, args []string) error {
	payload, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), string(cursorGuardUnreadable))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(cursorGuardDecision(payload)))
	return nil
}

// cursorGuardDecision returns the JSON decision for one Cursor payload.
//
// The payload is PARSED, not scanned. tool_input is written by the model and
// RunOS write tools take free-form string maps, so the model chooses key names
// inside it. A text scan for "mcp_server_name" cannot tell the real top-level
// key from a decoy nested in tool_input, and the sed-based scan that shipped
// before this one answered allow for a runos-write call carrying
// envVars: {"mcp_server_name": "runos"}. Only a JSON reader sees the nesting.
func cursorGuardDecision(payload []byte) []byte {
	names, ok := topLevelServerNames(payload)
	if !ok || len(names) == 0 {
		return cursorGuardUnreadable
	}

	// MOST RESTRICTIVE WINS across every name found. A payload carrying more than
	// one is already anomalous, so the safe reading is the riskiest one.
	var riskiest *cursorMCPServer
	for i := range names {
		server := classifyCursorServer(names[i])
		if server == nil {
			continue // a server that is not RunOS at all: not ours to judge
		}
		if server.risk == "" {
			continue // the RunOS read server carries no risk of its own
		}
		if riskiest == nil {
			riskiest = server
		}
	}
	if riskiest != nil {
		return cursorGuardAsk(*riskiest)
	}
	for i := range names {
		if classifyCursorServer(names[i]) != nil {
			return cursorGuardAllow // a known RunOS server with no risk
		}
	}
	return cursorGuardAllow // somebody else's MCP server
}

// topLevelServerNames returns EVERY top-level value whose key is mcp_server_name,
// compared case-insensitively, in document order.
//
// It walks the token stream rather than unmarshalling into a struct, because
// encoding/json hides two bypasses that were both reproduced against the real
// generated hook script:
//
//	{"mcp_server_name":"runos-write","mcp_server_name":"runos"}  -> allow
//	{"mcp_server_name":"runos-write","MCP_SERVER_NAME":"runos"}  -> allow
//
// Duplicate keys OVERWRITE, so the last one wins, and the decoder falls back to a
// case-insensitive field match, so MCP_SERVER_NAME binds to the same field. Either
// way a caller appends one key and downgrades ask to allow. Reversing the order
// gives ask, which proves it is strictly last-wins rather than anything subtler.
//
// Walking the tokens sees every occurrence, and the caller then takes the most
// restrictive. Depth is tracked so a decoy nested in tool_input is ignored, which
// is the bypass the previous version already closed and must keep closed.
func topLevelServerNames(payload []byte) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if delim, isDelim := tok.(json.Delim); !isDelim || delim != '{' {
		return nil, false
	}

	var names []string
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, false
		}
		key, isString := keyTok.(string)
		if !isString {
			return nil, false
		}
		if !strings.EqualFold(key, "mcp_server_name") {
			// Consume the value whatever its shape, so the walk stays aligned.
			var skip json.RawMessage
			if decErr := dec.Decode(&skip); decErr != nil {
				return nil, false
			}
			continue
		}
		var value json.RawMessage
		if decErr := dec.Decode(&value); decErr != nil {
			return nil, false
		}
		// An explicit null must NOT read as an empty name. json.Unmarshal of
		// `null` into a string SUCCEEDS and leaves the string empty, so without
		// this check a null server name classifies as "not RunOS" and is allowed.
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		var name string
		if strErr := json.Unmarshal(value, &name); strErr != nil {
			// Present but not a string. That is unreadable, not absent.
			return nil, false
		}
		names = append(names, name)
	}
	return names, true
}

// classifyCursorServer maps a server name to its RunOS entry, or nil when the name
// is not a RunOS server at all.
//
// An exact match on one of the four is preferred. Anything else that still looks
// like a RunOS write or sensitive server is treated as the highest risk, because
// falling through to allow was a real bypass. Reproduced against the real generated
// script: runos-writer, runos_write, "runos-write " with a trailing space,
// RUNOS-WRITE and runos-write-prod ALL returned allow. That last one is a plausible
// real configuration rather than a lab shape: a second account, or a copied and
// renamed entry, gave a fully live write server with no prompt on any call. The
// plugin's own shell guard already answered ask to all five, so the CLI guard was
// the weaker of the two.
func classifyCursorServer(name string) *cursorMCPServer {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	for i := range cursorMCPServers {
		if cursorMCPServers[i].name == trimmed {
			return &cursorMCPServers[i]
		}
	}
	if !strings.HasPrefix(trimmed, "runos") {
		return nil
	}
	if strings.Contains(trimmed, "sensitive") || strings.Contains(trimmed, "write") {
		return &cursorUnknownRunOSServer
	}
	return nil
}

var (
	cursorGuardAllow = cursorGuardJSON(map[string]string{"permission": "allow"})

	cursorGuardUnreadable = cursorGuardJSON(map[string]string{
		"permission":    "ask",
		"user_message":  "The RunOS guard could not read the MCP server name from this call, so it cannot tell a read from a write.",
		"agent_message": "The RunOS guard could not read the MCP server name from this call. Ask the user before you continue.",
	})

	// cursorGuardUnavailable is the script's own fallback, for when the runos
	// binary is gone or answers nothing. It is a different failure from an
	// unreadable payload and gets a different sentence, because the remedy is
	// different too.
	cursorGuardUnavailable = cursorGuardJSON(map[string]string{
		"permission":    "ask",
		"user_message":  "The RunOS guard could not reach the runos binary, so it cannot tell a read call from a write call. Run `runos mcp configure cursor` to repair it.",
		"agent_message": "The RunOS guard could not reach the runos binary and cannot judge this call. Ask the user before you continue.",
	})
)

func cursorGuardAsk(server cursorMCPServer) []byte {
	return cursorGuardJSON(map[string]string{
		"permission":    "ask",
		"user_message":  fmt.Sprintf("The %s server %s.", server.name, server.risk),
		"agent_message": fmt.Sprintf("The %s server %s. Ask the user before you continue.", server.name, server.risk),
	})
}

// cursorGuardJSON marshals one decision. A map[string]string cannot fail to
// marshal, so the error is impossible rather than merely unlikely.
func cursorGuardJSON(decision map[string]string) []byte {
	encoded, _ := json.Marshal(decision)
	return encoded
}

// cursorGuardScript builds the hook script Cursor runs.
//
// The script parses nothing. It hands the payload to the runos binary, whose
// JSON reader makes the decision. runosPath is absolute and baked in, because
// Cursor runs a project hook from the project root and promises nothing about
// PATH.
//
// Every failure path here answers ask, never allow: a missing binary, a binary
// that exits non-zero, a binary that prints nothing.
func cursorGuardScript(runosPath string) string {
	return fmt.Sprintf(`#!/bin/bash
# RunOS guard for Cursor, written by 'runos mcp configure cursor'. Cursor runs
# this before every MCP tool call and takes the permission decision from stdout.
#
# Re-run 'runos mcp configure cursor' to rewrite this file. Editing it by hand
# is pointless: the next configure run replaces it.
#
# The decision is made by the runos binary, not by this script. The payload
# carries tool_input, which the model writes, so a decoy "mcp_server_name"
# nested inside it must not be mistaken for the real top-level key. Only a JSON
# parser tells those apart.
set -uo pipefail

RUNOS_BIN=%s

if [ -x "$RUNOS_BIN" ]; then
  decision=$("$RUNOS_BIN" mcp cursor-guard 2>/dev/null)
  if [ -n "$decision" ]; then
    printf '%%s\n' "$decision"
    exit 0
  fi
fi

# The binary is gone, or it answered nothing. Ask rather than allow, and say so,
# because the one useful instruction is to run configure again.
printf '%%s\n' %s
exit 0
`, cursorShellQuote(runosPath), cursorShellQuote(string(cursorGuardUnavailable)))
}

// cursorShellQuote wraps a value so bash reads it as one word, whatever is in
// it. internal/vmconsole has the same three lines for ssh argument building;
// they are not shared because neither package should import the other for a
// one-liner.
func cursorShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
