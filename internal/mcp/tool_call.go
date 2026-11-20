package mcp

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/runos-official/cli/version"
)

func (s *Server) handleToolsCall(req *Request) *Response {
	var params CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    -32602,
				Message: "Invalid params",
				Data:    err.Error(),
			},
		}
	}

	if !s.toolAllowedInTier(params.Name) {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Tool %q is unavailable in the %s tier. Use tools/list to see tools for this server.", params.Name, s.category)}},
				IsError: true,
			},
		}
	}

	// The CATALOG sits ahead of the gates. It is documentation about the
	// command surface, it is what an agent needs BEFORE it can act, and
	// gating the map behind the territory is a lockout.
	//
	// `runos` DOES NOT. It executes. An earlier version of this block
	// returned early for both, which silently removed the documentation gate
	// on the gateway path, and the comment claimed it did not. The gate
	// exists because agents were hallucinating RunOS commands, so exec stays
	// behind it.

	// Bootstrap gate: handle mcp_bootstrap specially and enforce bootstrap-first on read server
	if params.Name == bootstrapToolName {
		// Bootstrap carries `cliUpdate`, which is what removed the separate
		// version-check round trip from every session. Conductor cannot tell
		// what binary is calling unless we say so, and the model must never
		// be asked to supply it. This is the ONLY dispatch point for
		// mcp_bootstrap: the generic injection further down never runs for it.
		if params.Arguments == nil {
			params.Arguments = make(map[string]any)
		}
		params.Arguments["version"] = version.Version
		params.Arguments["os"] = runtime.GOOS
		result, err := s.executor.Execute(params.Name, params.Arguments)
		if err != nil {
			// An attempt that failed is what downgrades the gate below. The
			// caller did as it was told; the credential or the API is what
			// is broken, and refusing every later call teaches it nothing
			// (review 2 item 2).
			s.bootstrapFailed = true
			s.bootstrapErr = err.Error()
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentBlock{{Type: "text", Text: err.Error()}},
					IsError: true,
				},
			}
		}
		s.bootstrapped = true
		s.bootstrapFailed = false
		s.bootstrapErr = ""
		s.topicKeys = topicKeysFromBootstrap(result)
		// Bootstrap returns the instructions every session must follow,
		// which is documentation. Counting it toward the read-at-least-N
		// gate matters most in the case that gate handles worst: a search
		// that comes back empty leaves the agent unable to open it at all
		// (B1).
		if s.topicsRead == nil {
			s.topicsRead = make(map[string]struct{})
		}
		s.topicsRead[bootstrapTopicKey] = struct{}{}
		content := []ContentBlock{{Type: "text", Text: result}}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  CallToolResult{Content: content},
		}
	}

	// The bootstrap gate runs on every server that can act, not just the
	// read one. Pre-fix the servers that CHANGE things were the two with
	// no gate at all, which is the wrong way round (goal 21 B16). Each
	// server is its own process with its own flag, so an agent calls
	// mcp_bootstrap once per server it uses; the tool is listed on all of
	// them so the gate is always satisfiable.
	//
	// A gate is only a gate while the caller can open it. Once a bootstrap
	// attempt has FAILED (expired sign-in, conductor unreachable), every
	// later refusal is a lockout of the whole server, so the gate becomes a
	// warning the caller carries on its results (review 2 item 2).
	gateWarning := ""
	if !s.bootstrapped && bootstrapRequired(s.category) && !isGateExemptTool(params.Name) {
		if !s.bootstrapFailed {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentBlock{{Type: "text", Text: "ERROR: You must call the mcp_bootstrap tool before using any other tools. Call mcp_bootstrap now (no arguments needed) to receive critical instructions for correct RunOS usage."}},
					IsError: true,
				},
			}
		}
		gateWarning = bootstrapGateWarning(s.bootstrapErr)
	}

	// Topic-reading tools (mcp_topics_search, mcp_topics_show) are always allowed
	// after bootstrap. Track the distinct topic keys the LLM consumes so we can
	// enforce the read-at-least-N-topics gate below.
	if params.Name == "mcp_topics_search" || params.Name == "mcp_topics_show" {
		result, err := s.executor.Execute(params.Name, params.Arguments)
		if err != nil {
			return &Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentBlock{{Type: "text", Text: err.Error()}},
					IsError: true,
				},
			}
		}
		// Client-side fallback: a search that found nothing is answered
		// from the bootstrap topic index by key match, rather than with
		// an empty list the caller cannot act on (B1).
		if params.Name == "mcp_topics_search" && searchReturnedNothing(result) {
			keywords, _ := params.Arguments["keywords"].(string)
			if matches := topicKeySuggestions(keywords, s.topicKeys); len(matches) > 0 {
				if fallback := topicFallbackResult(keywords, matches); fallback != "" {
					result = fallback
				}
			}
		}
		s.recordTopicsRead(params.Name, params.Arguments, result)
		// Reading the kafka topic and then calling a kafka tool that is not
		// listed is a confusing dead end. Every topic stays readable under
		// scoping, so the topic itself has to say the tools are one call away.
		notice := s.toolsets.hiddenTypeNotice(params.Arguments)
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: gateWarning + notice + result}},
			},
		}
	}

	// Topic-read gate: on the read server, require minTopicsRead distinct topic
	// keys to have been consumed before any non-topic, non-bootstrap tool runs.
	// It stands down for the same two reasons the bootstrap gate does: the
	// tools that open a gate are never behind it, and a bootstrap that
	// failed took the topic tools with it, so holding the gate shut would
	// leave the read server unusable rather than uninformed.
	if s.category == "read" && len(s.topicsRead) < minTopicsRead && !isGateExemptTool(params.Name) && !s.bootstrapFailed {
		msg := fmt.Sprintf(
			"ERROR: You have read %d/%d required documents. Before using other tools, READ at least %d documents relevant to the user's task: mcp_bootstrap counts as one, and each mcp_topics_show counts as one. Call mcp_topics_show with an exact key from the bootstrap topic index. mcp_topics_search finds the key by keywords (e.g. \"deploy\", \"postgresql\", \"dockerfile\") but does not read anything, so it does not count. This ensures you follow correct RunOS procedures instead of guessing.",
			len(s.topicsRead), minTopicsRead, minTopicsRead,
		)
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: msg}},
				IsError: true,
			},
		}
	}

	var result string
	var err error

	// Special handling for deploy (calls runos deploy subprocess)
	if params.Name == "deploy" {
		result, err = s.handleDeploy(params.Arguments)
	} else if isManifestUpdateTool(params.Name) {
		var changed bool
		result, changed, err = s.handleManifestUpdate()
		// Only a list that actually changed is announced. Notifying on
		// every refresh made the client re-read hundreds of tool
		// definitions to find nothing new (review 2 item 22).
		if err == nil && changed {
			s.sendNotification("notifications/tools/list_changed")
		}
	} else if params.Name == toolsEnableToolName {
		var changed bool
		result, changed, err = s.handleToolsEnable(params.Arguments)
		// Same rule as manifest_update: announce only a list that really
		// changed, so the client does not re-read every definition to
		// find nothing new.
		if err == nil && changed {
			s.sendNotification("notifications/tools/list_changed")
		}
	} else if isModuleToggleTool(params.Name) {
		// A module toggle changes what this account may call, so the tool
		// list changes with it. See module_toggle.go.
		result, err = s.handleModuleToggle(params.Name, params.Arguments)
	} else if isStaticRunTool(params.Name) {
		result, err = s.handleRun(params.Arguments)
	} else if isStaticAppsTool(params.Name) {
		// Static apps subcommands (pull, diff, sync, list-previous-uploads)
		// are not in the manifest; they orchestrate local filesystem
		// alongside API calls and run as runos subprocesses.
		result, err = s.handleAppsCommand(params.Name, params.Arguments)
	} else if isStaticServicesTool(params.Name) {
		// Static services subcommands (pull, diff, sync) follow the same
		// pattern: yaml-file driven, manifest-aware, run via subprocess.
		result, err = s.handleServicesCommand(params.Name, params.Arguments)
	} else if isStaticJobsTool(params.Name) {
		// jobs_follow blocks the subprocess until the job terminates;
		// MCP caller gets the full streamed log + final state in one
		// text payload. Cheaper than polling jobs_show in a loop.
		result, err = s.handleJobsCommand(params.Name, params.Arguments)
	} else {
		// Auto-inject CLI version and OS for version check tool.
		// (mcp_bootstrap needs the same, but is dispatched earlier and
		// injects there.)
		if params.Name == "cli_version-check" {
			if params.Arguments == nil {
				params.Arguments = make(map[string]any)
			}
			params.Arguments["version"] = version.Version
			params.Arguments["os"] = runtime.GOOS
		}
		// All other tools go to executor
		result, err = s.executor.Execute(params.Name, params.Arguments)
	}

	// I13-A: warn when the on-disk binary has been rebuilt since this
	// MCP server started. The warning prefixes every subsequent tool
	// response (success and error paths) so the operator notices the
	// drift instead of silently consuming stale cli_version-check
	// answers / out-of-date tool descriptions. Once tripped, the flag
	// stays sticky for the session.
	stalePrefix := gateWarning
	if s.checkStaleBinary() {
		stalePrefix += s.staleBinaryWarning()
	}

	if err != nil {
		// A 4xx can be manifest drift rather than a bad request: a route
		// this server still remembers and conductor has renamed. The CLI
		// says so on its own dispatch path; MCP said nothing (B7).
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: stalePrefix + err.Error() + s.manifestDriftNote(err)}},
				IsError: true,
			},
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: CallToolResult{
			Content: []ContentBlock{{Type: "text", Text: stalePrefix + result}},
		},
	}
}
