// Package mcp implements a Model Context Protocol server for AI assistant integration.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/version"
)

// placeholderRegex matches {name} patterns in command paths
var placeholderRegex = regexp.MustCompile(`/?\{[^}]+\}`)

// Request represents a JSON-RPC 2.0 request message.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response represents a JSON-RPC 2.0 response message.
type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Error represents a JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ServerInfo describes the MCP server's name and version.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult represents the response to an MCP initialize request.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

// Capabilities describes the features supported by the MCP server.
type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability describes tool-related capabilities of the server.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Tool represents an MCP tool definition with its name, description, and input schema.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema InputSchema `json:"inputSchema"`
	// Annotations carries the MCP tool hints. Omitted when nil, because an
	// absent block and an all-false block do not mean the same thing to a
	// client: absent is "unknown", false is "asserted to write".
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations are the MCP tool hints a client uses to tell a read from a
// write. Without them a client cannot classify a tool and must assume the
// unsafe case.
//
// WHY THIS EXISTS. Cursor's per-server panel carries a "Writes" policy. With no
// annotation it treated all 315 tools on the READ server as writes, so setting
// that policy to "Don't allow" disabled every one of them and the panel read
// "0 tools enabled". A user picking the cautious setting lost the entire
// read-only surface, which is the opposite of what the four-server split is
// there to communicate.
//
// These are HINTS and are not load-bearing for access control. The per-server
// tool allow-list remains the actual boundary; a client is free to ignore
// anything here.
type ToolAnnotations struct {
	// ReadOnlyHint is true when the tool does not modify any environment.
	// Derived from the manifest method: a GET changes nothing.
	ReadOnlyHint bool `json:"readOnlyHint,omitempty"`
	// DestructiveHint is true when the tool may perform an irreversible
	// change. Only meaningful when ReadOnlyHint is false.
	DestructiveHint bool `json:"destructiveHint,omitempty"`
}

// annotationsFor derives the MCP hints from the manifest entry. The manifest
// already carries everything needed: the HTTP method says whether the call
// changes anything, and dynacmd owns the destructive rule the CLI's own --yes
// gate uses, so the hint cannot drift from the confirmation it mirrors.
func annotationsFor(cmd manifest.Command) *ToolAnnotations {
	// The SERVING TIER is the authority, not the HTTP method. The per-server
	// allow-list is the real access-control boundary, so a command carried by
	// a read tier cannot write whatever verb it uses. Several genuine reads
	// are POSTs because they need a request body: virt/config-diff and
	// storage-groups/inspect-device are both POST and both tier `read`.
	// Keying on the method alone left those seven marked as writes.
	write, read := false, false
	for _, tier := range cmd.MCP {
		switch tier {
		case "write", "sensitive_write":
			write = true
		case "read", "sensitive_read":
			read = true
		}
	}
	if !write && read {
		return &ToolAnnotations{ReadOnlyHint: true}
	}
	if !write && !read && strings.EqualFold(cmd.Method, "GET") {
		// No tier declared. Fall back to the method, which is still sound:
		// a GET changes nothing.
		return &ToolAnnotations{ReadOnlyHint: true}
	}
	if dynacmd.IsDestructiveCommand(cmd) {
		return &ToolAnnotations{DestructiveHint: true}
	}
	return &ToolAnnotations{}
}

// InputSchema defines the JSON Schema for a tool's input parameters.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property describes a single property within an InputSchema.
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
	// AdditionalProperties is set when Type=="object" to advertise the
	// shape of values inside the map. Without it, some LLM client
	// libraries fall back to stringifying the whole argument and
	// downstream validation rejects it as "not an object". Mirrors how
	// JSON Schema describes string-keyed maps.
	AdditionalProperties *Property `json:"additionalProperties,omitempty"`
	// Items is set when Type=="array" to declare the element type.
	// Same motivation as AdditionalProperties.
	Items *Property `json:"items,omitempty"`
	// Properties and Required are populated when Type=="object" and the
	// element shape is known (manifest field carries `itemFields`). Used
	// for array-of-object fields like apps/secret-files/update.add so an
	// LLM that follows the schema strictly passes objects, not strings.
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification: a method with no id, so
// the receiver sends no response. The server emits
// `notifications/tools/list_changed` after a manifest refresh.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
}

// ToolsListResult represents the response to a tools/list request.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// CallToolParams represents the parameters for a tools/call request.
type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// CallToolResult represents the result of a tools/call request.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock represents a single content block in a tool call result.
// Text MUST NOT be tagged `omitempty`: the MCP spec requires every text
// block to carry a string `text` field, and conformant clients reject
// `{"type":"text"}` with a Zod `invalid_union` error. An empty success
// frame is emitted as `{"type":"text","text":""}`; the executor turns
// empty-body 2xx responses into a deterministic success string before
// wrapping, so this branch should not fire in practice (foreman #78).
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// minTopicsRead is the number of distinct documentation topics the LLM must
// consume (via mcp_topics_search or mcp_topics_show) after bootstrap before
// other tools become available on the read server. Kept low deliberately:
// platform-overview plus one task topic is enough context, and the bootstrap's
// topic router pulls further reads in naturally via `see also:` links.
// minTopicsRead counts mcp_bootstrap itself, so 1 means "bootstrap only" and
// no extra topic read is forced.
//
// WAS 2. The gate exists because agents hallucinated RunOS commands, and it
// forced one topic read on top of bootstrap. It was raised to 2 when
// mcp_bootstrap was delivering an EMPTY instructions string: the agent got 75
// bare topic keys and nothing else, so a second read was the only way it saw
// any rule at all. Measured 2026-09-02, conductor dev served instructions ""
// while foreman held 8,653 characters of them.
//
// With that pipeline repaired the bootstrap now carries the rules the gate was
// standing in for: "read the topic before you claim", "NO hallucinating
// services", and the opening-reads router that says WHICH topic to read for a
// task. Forcing a second read no longer adds a rule the agent has not seen; it
// adds a round trip, and a round trip re-sends the whole conversation.
//
// If hallucination returns, raise this before adding anything else: it is the
// cheapest lever here and it is one line.
const minTopicsRead = 1

// Server instructions by category
var serverInstructions = map[string]string{
	"read": `RunOS MCP Server (Read-Only)

Query clusters, services, apps, and infrastructure state. No modifications.

REQUIRED FIRST STEP:
Call mcp_bootstrap. It returns the instructions every session must follow, the topic
index, and whether this CLI is behind. Then follow what it says; it is the only source
of truth for the opening sequence. Read a topic with mcp_topics_show when the task needs
one (mcp_topics_search finds the key).

Do not guess or invent values. The documentation tells you the correct ones.`,

	"sensitive_read": `RunOS MCP Server (Sensitive Read)

Access sensitive data like credentials, connection strings, and secrets. Data returned will be visible to the LLM.

IMPORTANT: Ensure you have called mcp_bootstrap on the runos (read-only) server before using these tools.`,

	"write": `RunOS MCP Server (Write)

Create, update, and manage clusters, services, and applications. Changes affect live infrastructure.

IMPORTANT: Ensure you have called mcp_bootstrap on the runos (read-only) server before using these tools.`,

	"sensitive_write": `RunOS MCP Server (Sensitive Write)

Perform sensitive write operations like credential rotation and secret management. Changes affect live infrastructure and security-critical data.

IMPORTANT: Ensure you have called mcp_bootstrap on the runos (read-only) server before using these tools.`,
}

// Server is the MCP server that handles JSON-RPC requests over stdio.
type Server struct {
	manifest     *manifest.Manifest
	executor     ToolExecutor
	version      string
	category     string // "read", "sensitive_read", "write", "sensitive_write"
	bootstrapped bool   // true after mcp_bootstrap has been called successfully
	// bootstrapFailed is true once an mcp_bootstrap attempt has come back
	// with an error and none has since succeeded. It downgrades the
	// bootstrap gate from a refusal to a warning, because a caller that
	// cannot bootstrap cannot open the gate either (review 2 item 2).
	bootstrapFailed bool
	// bootstrapErr is the last bootstrap failure, repeated in that warning
	// so the caller sees WHY the instructions are missing.
	bootstrapErr string
	// topicsRead is the set of distinct topic keys the LLM has consumed via
	// mcp_topics_search or mcp_topics_show during this session. Used to gate
	// non-topic tools on the read server until minTopicsRead is reached.
	topicsRead map[string]struct{}
	// startupBinaryMtime records the mtime of the running binary at MCP
	// server start. When the on-disk binary's mtime advances past this
	// (i.e. the user rebuilt the CLI while this MCP process kept running),
	// every subsequent tool response is prefixed with a stale-binary
	// warning so the LLM/operator notices the version drift instead of
	// silently getting stale answers from cli_version-check etc.
	// Regression target: I13-A.
	startupBinaryMtime  time.Time
	startupBinaryPath   string
	staleBinaryDetected bool
	// topicKeys is the topic index mcp_bootstrap returned. Kept so a
	// keyword search that finds nothing can fall back to matching the
	// caller's words against the keys (B1).
	topicKeys []string
	// reloader refreshes the manifest without restarting the server. Nil
	// disables the refresh paths (B2).
	reloader ManifestReloader
	// driftChecked keeps the 4xx manifest-drift comparison to one network
	// call per process (B7).
	driftChecked bool
	// lastVersionProbe is when the tools/list version check last asked the
	// API. The probe carries the manifest loader's 10 s timeout, so an
	// unreachable API used to stall every tools/list for 10 s (review 2
	// item 22).
	lastVersionProbe time.Time
	// defaultClusterID is the cluster the CLI falls back to when a tool
	// call names none. Empty means there is no fallback, and then a
	// cluster-scoped tool genuinely REQUIRES cid: the schema says so
	// rather than letting the call fail at dispatch with "cluster ID
	// required" (goal 21 B13).
	defaultClusterID string
	// toolsets narrows the managed-service tools to the types this account
	// actually runs. Never nil; an unscoped Toolsets exposes everything.
	toolsets *Toolsets
}

// SetDefaultClusterID records the configured default cluster, so the
// tool schema can mark `cid` required only when there is no fallback.
func (s *Server) SetDefaultClusterID(cid string) {
	s.defaultClusterID = cid
}

// ToolExecutor defines the interface for executing MCP tools against the API.
type ToolExecutor interface {
	Execute(toolName string, args map[string]any) (string, error)
	ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error)
}

// NewServer creates a new MCP server with the given manifest, executor, version, and category.
func NewServer(m *manifest.Manifest, executor ToolExecutor, version, category string) *Server {
	s := &Server{
		manifest: m,
		executor: executor,
		version:  version,
		category: category,
		// Scoped to the service types this account runs, when a cache says
		// which. Unreadable or absent means unscoped: never hide an
		// operator's own platform because a cache file went missing.
		toolsets: NewToolsets(m),
	}
	// Capture the running binary's mtime at startup so subsequent tool
	// calls can detect a rebuilt-while-running situation (I13-A). Best
	// effort: a missing or unreadable executable leaves the fields at
	// their zero value, which checkStaleBinary treats as "can't tell"
	// and silently skips the warning.
	if path, err := os.Executable(); err == nil {
		if info, err := os.Stat(path); err == nil {
			s.startupBinaryPath = path
			s.startupBinaryMtime = info.ModTime()
		}
	}
	return s
}

// checkStaleBinary stats the running binary and returns true when its
// mtime has advanced past the recorded startup mtime. Once true, the
// flag stays sticky for the rest of the session so every subsequent
// response carries the warning. Returns false when the path was never
// captured (e.g. os.Executable failed at startup), avoiding a false
// positive on the first call. Regression target: I13-A.
func (s *Server) checkStaleBinary() bool {
	if s.staleBinaryDetected {
		return true
	}
	if s.startupBinaryPath == "" || s.startupBinaryMtime.IsZero() {
		return false
	}
	info, err := os.Stat(s.startupBinaryPath)
	if err != nil {
		return false
	}
	if info.ModTime().After(s.startupBinaryMtime) {
		s.staleBinaryDetected = true
		return true
	}
	return false
}

// staleBinaryWarning returns the prefix to inject into every MCP tool
// response when the binary has been rebuilt mid-session. Kept as a
// helper so the wording is single-sourced. Single line so it doesn't
// dwarf the actual response in transcripts.
func (s *Server) staleBinaryWarning() string {
	return "[runos-mcp warning] this MCP server is running an outdated runos binary (rebuilt at " + s.startupBinaryPath + " after the server started). Restart the MCP server / IDE host to pick up the new binary; responses below were produced by the older code path.\n\n"
}

// Run starts the MCP server, reading JSON-RPC requests from stdin and writing responses to stdout.
func (s *Server) Run() error {
	reader := bufio.NewReader(os.Stdin)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.sendError(nil, -32700, "Parse error", err.Error())
			continue
		}

		resp := s.handleRequest(&req)
		if resp != nil {
			s.sendResponse(resp)
		}
	}
}

func (s *Server) handleRequest(req *Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized", "notifications/initialized":
		// Notification, no response needed (Codex sends notifications/initialized)
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/templates/list":
		return s.handleResourcesTemplatesList(req)
	case "ping":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &Error{
				Code:    -32601,
				Message: "Method not found",
			},
		}
	}
}

func (s *Server) getInstructions() string {
	if instructions, ok := serverInstructions[s.category]; ok {
		return instructions
	}
	return serverInstructions["read"]
}

func (s *Server) getServerName() string {
	switch s.category {
	case "read":
		return "runos"
	case "sensitive_read":
		return "runos-sensitive-read"
	case "write":
		return "runos-write"
	case "sensitive_write":
		return "runos-sensitive-write"
	default:
		return "runos"
	}
}

func (s *Server) handleInitialize(req *Request) *Response {
	// Extract client's protocol version and echo it back (Codex compatibility)
	protocolVersion := "2024-11-05" // default
	if req.Params != nil {
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(req.Params, &params); err == nil && params.ProtocolVersion != "" {
			protocolVersion = params.ProtocolVersion
		}
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: InitializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities: Capabilities{
				// listChanged: manifest_update and the tools/list version
				// re-check both change the tool list mid-session, so the
				// client has to be told it may need to re-read it (B2).
				Tools: &ToolsCapability{ListChanged: true},
			},
			ServerInfo: ServerInfo{
				Name:    s.getServerName(),
				Version: s.version,
			},
			Instructions: s.getInstructions(),
		},
	}
}

func (s *Server) handleToolsList(req *Request) *Response {
	// A client asking what exists is the right moment to check whether
	// the answer has changed since startup (B2). Silent on failure: an
	// offline version check must not break tools/list.
	s.refreshManifestIfDrifted()
	var tools []Tool
	tools = s.buildTools()
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: ToolsListResult{
			Tools: tools,
		},
	}
}

// handleResourcesList returns an empty resources list (Codex compatibility)
func (s *Server) handleResourcesList(req *Request) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"resources": []any{},
		},
	}
}

// handleResourcesTemplatesList returns an empty resource templates list (Codex compatibility)
func (s *Server) handleResourcesTemplatesList(req *Request) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"resourceTemplates": []any{},
		},
	}
}

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

// recordTopicsRead records the distinct topic keys the LLM has actually
// read.
//
// Only mcp_topics_show counts. A search answers with keys, titles and
// content LENGTHS, so counting its hits opened the whole read server on
// one call that delivered no documentation at all, and a search matching
// two topics satisfied a gate that exists to make the agent read two
// (review 2 item 18). The gate stays satisfiable in one step: mcp_bootstrap
// counts as one read, mcp_topics_show is never gated, and the bootstrap
// hands over the key index to show.
func (s *Server) recordTopicsRead(toolName string, args map[string]any, result string) {
	if toolName != "mcp_topics_show" {
		return
	}
	if s.topicsRead == nil {
		s.topicsRead = make(map[string]struct{})
	}
	if k, ok := args["key"].(string); ok && k != "" {
		s.topicsRead[k] = struct{}{}
	}
}

func (s *Server) handleDeploy(args map[string]any) (string, error) {
	// Get the runos executable path (same binary that's running this MCP server)
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	cmdArgs := buildDeployArgs(args)

	// Execute the deploy command with a 10-minute timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	// Combine output
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if err != nil {
		if output == "" {
			return "", fmt.Errorf("deploy failed: %w", err)
		}
		return "", fmt.Errorf("deploy failed: %s", output)
	}

	return output, nil
}

// contains checks if a string slice contains a specific item
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// cidDescription is the one sentence every cluster-scoped tool carries.
func cidDescription(hasDefault bool) string {
	if hasDefault {
		return "Cluster id. Omit to use the configured default cluster."
	}
	return "Cluster id."
}

func (s *Server) buildTools() []Tool {
	var tools []Tool

	for _, cmd := range s.manifest.Commands {
		// Skip if command doesn't belong to this MCP server category.
		// The bootstrap and topic tools are the exception: every server
		// gates on bootstrap (B16), so every server has to list the tool
		// that satisfies the gate and the topic tools it points at.
		if !contains(cmd.MCP, s.category) && !isGateTool(cmd.Command) {
			continue
		}

		// Tool name = command path with {id} placeholders stripped, then / replaced by _
		// e.g., "services/minio/{id}/show" -> "services/minio/show" -> "services_minio_show"
		cmdPath := placeholderRegex.ReplaceAllString(cmd.Command, "")
		toolName := strings.ReplaceAll(cmdPath, "/", "_")

		// Skip any tool that has a hand-written static shim. The conductor
		// may register a manifest entry for services/harbor/build-image (for
		// discoverability + the CLI verb), but the build context is a local
		// filesystem path the manifest can't express, so the real tool is
		// the filesystem-aware shim appended via staticServicesTools. A
		// manifest-driven version here would be a filesystem-blind duplicate.
		if isStaticServicesTool(toolName) || isStaticAppsTool(toolName) {
			continue
		}

		// Managed-service tools for a type this account does not run are
		// left out. runos_tools_enable adds one back mid-session.
		if !s.toolsets.permits(cmd.Command) {
			continue
		}

		// Skip deploy - it has a built-in handler
		if toolName == "deploy" {
			// Add deploy tool with custom description and cid parameter
			tools = append(tools, Tool{
				Name: "deploy",
				// Builds and rolls out to the cluster, so not read-only.
				Annotations: &ToolAnnotations{},
				Description: `Deploy an application. The CLI dispatches on the app's deployType:

  - CLI deploy (deployType: cli): tarballs the local source and uploads to /prepare-cli-deployment. The classic flow.
  - VCS deploy (deployType: vcs): no tarball. Conductor pulls source from the linked GitHub/GitLab integration at a specific commit sha, builds in-cluster, pushes, patches, waits for rollout. Trigger by passing sha (and optionally app to skip the local yaml load).

WHEN TO USE WHICH SHAPE:
  - CLI-deploy app, from a checkout: pass yaml_file (or rely on the single-runos*.yaml auto-detect). Do NOT pass sha/app/allow_dirty (rejected for CLI-deploy apps).
  - VCS-deploy app, from a checkout: pass yaml_file. sha defaults to git rev-parse HEAD; pass sha to deploy a specific commit. allow_dirty waives the dirty-tree refusal.
  - VCS-deploy app, no checkout (CI mode, or fast "deploy by id" from a laptop): pass app=<5-char id> and sha=<commit>. Skips the yaml load entirely.

DIRTY-TREE GATE (VCS only): when sha is auto-resolved from HEAD and the working tree has uncommitted changes, deploy refuses. Pass allow_dirty=true to proceed. The build uses the COMMITTED source at HEAD, NOT the dirty edits. Recommend committing first unless the user has explicitly accepted the gap.

BEFORE CALLING (CLI-deploy only): check for large files (>1MB) or build artifacts that should be in .dockerignore to avoid slow uploads. Requires a Dockerfile that runs as non-root.

MULTI-YAML PROJECTS: a project directory can hold multiple runos.<cid>.<id>.yaml files (e.g. staging + prod sharing one source tree). When more than one runos*.yaml exists, you MUST pass yaml_file explicitly so the right manifest is deployed. Monorepos with VCS apps under per-app subdirs use configPath (set on apps_add) so the cluster agent reads the per-app yaml from the committed tree.

Pre-deploy drift gate (CLI-deploy only): when the yaml has id/cid/aid set, deploy first compares local against the running app on the server. If anything has changed on the server that isn't reflected locally, the deploy refuses with the diff in the error so the user can decide whether to overwrite. Pass force=true to bypass the gate and overwrite server state with the local version. Fresh yamls (no id) skip the gate entirely.

LEGACY YAML MIGRATION (CLI-deploy only): when the deploy refusal output contains "deprecated field names" or "deprecated fields (port:/domain:/standardHttps:)", the user's yaml uses the legacy schema (top-level port:/standardHttps:/domain: instead of servicePortMappings:[{port,standardHttps,domains:[{fqdn,enableCloudflareProxy}]}]). DO NOT recommend force=true in this case, forcing keeps the user on the legacy shape and the same drift returns on every deploy. Instead, recommend they run "runos apps pull <yaml-path> --force" (which rewrites the local file from the canonical server state), then re-call this deploy tool. Migration is one-time per yaml.

VCS deploys with an unchanged sha are near-instant: the orchestration short-circuits the build when the image is already in Harbor and only runs manifest reconcile + rollout watch. Use this to apply config drift after editing the yaml without a rebuild: edit yaml, commit, push, deploy with the new sha.

DOCKER BUILD ARGS (both deployTypes): pass one or more KEY=VALUE entries via the build_arg array to set Docker build args on the image build (most acutely Next.js bundles baking NEXT_PUBLIC_* values). Build args also accept a declarative source in runos.yaml under "buildArgs:" (committed, per-environment). Precedence: --build-arg > runos.yaml buildArgs. Conductor merges and forwards to BuildKit server-side; the CLI does NOT merge. Duplicate keys within build_arg are rejected with a clear error before any API call. Keys must match the Docker ARG name regex (^[A-Za-z_][A-Za-z0-9_]*$). Undeclared ARGs in the Dockerfile are ignored (no-op, safe). Values are plaintext; do not put secrets here.`,
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]Property{
						"cid": {
							Type:        "string",
							Description: "Cluster id to deploy to. Required when the CLI has no default cluster.",
						},
						"yaml_file": {
							Type:        "string",
							Description: "Path to the runos*.yaml manifest to deploy. Optional when only one runos*.yaml is present in the project directory. REQUIRED when the directory holds multiple manifests (multi-cluster projects), otherwise the wrong app may be deployed. The CLI subprocess loads this via the --config flag. For VCS apps, the local yaml is consulted only for the app id; the committed yaml at <sha> is the source of truth for the build.",
						},
						"app": {
							Type:        "string",
							Description: "VCS-deploy only. App ID (5-char identifier) to deploy when running without a runos.yaml on disk (CI mode, or fast laptop deploys by id). Cannot be combined with yaml_file. Rejected for CLI-deploy apps.",
						},
						"sha": {
							Type:        "string",
							Description: "VCS-deploy only. Commit SHA (7-40 hex chars) to deploy. Defaults to `git rev-parse HEAD` when omitted and a checkout is present. Rejected for CLI-deploy apps. The cluster agent reads the committed runos.yaml at this sha for the build.",
						},
						"allow_dirty": {
							Type:        "boolean",
							Description: "VCS-deploy only. Waive the dirty-tree refusal when sha is auto-resolved from HEAD. The build uses the committed source at HEAD, NOT uncommitted edits. Recommend committing first unless the user has explicitly accepted the gap.",
							Default:     false,
						},
						"force": {
							Type:        "boolean",
							Description: "CLI-deploy only. Bypass the pre-deploy drift gate and overwrite server state with the local version. Only set this after the user has reviewed the drift and explicitly chosen to proceed. DO NOT set this when the gate output mentions deprecated/legacy fields, recommend `apps_pull <yaml> force=true` to migrate first.",
							Default:     false,
						},
						"build_arg": {
							Type:        "array",
							Description: "Docker build args as KEY=VALUE strings (repeatable, applies to both deployTypes). Merged server-side with runos.yaml `buildArgs:`; --build-arg wins. Duplicate keys rejected. Keys must match the Docker ARG name regex (^[A-Za-z_][A-Za-z0-9_]*$). Values are plaintext, do not pass secrets here.",
							Items:       &Property{Type: "string"},
						},
					},
				},
			})
			continue
		}

		tool := Tool{
			Name:        toolName,
			Description: cmd.Description,
			InputSchema: InputSchema{
				Type:       "object",
				Properties: make(map[string]Property),
			},
			Annotations: annotationsFor(cmd),
		}

		// Add cid parameter if endpoint requires it. It is REQUIRED when
		// the CLI has no default cluster to fall back on: saying optional
		// there made the call fail at dispatch with "cluster ID required"
		// for a value the schema said could be left out (B13).
		if strings.Contains(cmd.Endpoint, ":cid") {
			tool.InputSchema.Properties["cid"] = Property{
				Type: "string",
				// One short sentence. Repeated on every cluster-scoped tool (259
				// of 315 on the read server); the 240-character version it
				// replaces was 26% of the whole server. Whether cid may be
				// omitted is stated STRUCTURALLY by `required` below, and
				// "use clusters_list" lives in the bootstrap.
				Description: cidDescription(s.defaultClusterID != ""),
			}
			if s.defaultClusterID == "" {
				tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, "cid")
			}
		}

		// Destructive verbs take an explicit confirm, mirroring the
		// CLI's --yes. Pre-fix the CLI refused a delete that no human
		// could confirm while MCP dispatched the same delete on the
		// first ask, so the safer surface was the one a person drives
		// and the unguarded one was the LLM's (A14).
		if dynacmd.IsDestructiveCommand(cmd) {
			tool.InputSchema.Properties[confirmArgName] = Property{
				Type:        "boolean",
				Description: "Must be true. This verb is destructive and cannot be undone: set confirm=true only after the user has agreed to this exact target. The CLI asks a human the same question through --yes.",
			}
			tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, confirmArgName)
			// The description is what an LLM reads while it decides what to
			// call. Saying it only in the property description cost a
			// round trip on every destructive verb (review 2 item 6).
			tool.Description = withConfirmNotice(cmd.Description)
		}

		if cmd.Input != nil {
			for _, field := range cmd.Input.Fields {
				prop := Property{
					Type:        s.mapType(field.Type),
					Description: field.Description,
				}
				if len(field.Enum) > 0 {
					prop.Enum = field.Enum
				}
				if field.Default != nil {
					prop.Default = field.Default
				}
				// JSON Schema requires `additionalProperties` (for object) and
				// `items` (for array) so clients know what to put inside the
				// container. The manifest now carries optional `valueType` +
				// `valueFields` for object fields (parallel to `itemType` +
				// `itemFields` for arrays); when present, projectObjectValue
				// emits the richer schema. Otherwise we fall back to a
				// pre-existing carve-out table:
				//   - providerOptions (domains_create / domains_update):
				//     drop the additionalProperties constraint entirely
				//     so booleans round-trip natively.
				//   - requires (apps/add / apps/update / deploy): map of
				//     alias → {id, type, config, env}; emit the structured
				//     value schema so MCP clients don't have to stringify.
				//     Regression target: I26-N.
				//   - everything else: default to string values for
				//     back-compat with existing map[string]string fields
				//     whose manifest entries pre-date valueType. LLM libs
				//     that strict-validate the schema (some versions of
				//     the Anthropic SDK tool wrapper) accept it.
				if prop.Type == "object" {
					prop.AdditionalProperties = s.projectObjectValue(field)
				}
				if prop.Type == "array" {
					prop.Items = s.projectArrayItems(field)
				}
				tool.InputSchema.Properties[field.Name] = prop

				if field.Required {
					tool.InputSchema.Required = requireOnce(tool.InputSchema.Required, field.Name)
				}
			}

			for _, flag := range cmd.Input.Flags {
				tool.InputSchema.Properties[flag.Name] = Property{
					Type:        "boolean",
					Description: flag.Description,
					Default:     flag.Default,
				}
			}
		}

		tools = append(tools, tool)
	}

	// Append static apps_* tools (pull/diff/sync/list-previous-uploads)
	// for the categories they belong to. These aren't manifest-driven.
	tools = append(tools, staticAppsTools(s.category)...)
	// Append static services_* tools (pull/diff/sync). Same rationale:
	// orchestrating local yaml files, not pure API operations.
	tools = append(tools, staticServicesTools(s.category)...)
	// Append jobs_follow — long-poll streaming until terminal state,
	// shape the manifest dispatcher doesn't model.
	tools = append(tools, staticJobsTools(s.category)...)
	// Append manifest_update: every category can be the one holding a
	// command list that predates the last conductor deploy (B2).
	tools = append(tools, manifestUpdateTool())
	// Append `run` — blocks streaming container output and exits with
	// the container's real exit code; a shape the manifest's one-call
	// dispatcher can't model. Only surfaces under sensitive_write
	// since it mutates cluster state.
	tools = append(tools, staticRunTools(s.category)...)
	// Only worth listing when something is actually hidden: an unscoped
	// server has nothing to add, and the tool would be dead weight.
	if s.toolsets.Scoped() && len(s.toolsets.Hidden()) > 0 {
		tools = append(tools, Tool{
			Name:        toolsEnableToolName,
			Description: s.toolsets.enableToolDescription(),
			InputSchema: InputSchema{
				Type: "object",
				Properties: map[string]Property{
					"types": {
						Type:        "array",
						Description: "Service types to load, e.g. [\"kafka\",\"vllm\"].",
						Items:       &Property{Type: "string"},
					},
				},
				Required: []string{"types"},
			},
			Annotations: &ToolAnnotations{ReadOnlyHint: true},
		})
	}

	return tools
}

func (s *Server) mapType(t string) string {
	switch t {
	case "integer":
		return "number"
	case "array":
		return "array"
	case "boolean":
		return "boolean"
	case "object":
		return "object"
	default:
		return "string"
	}
}

// projectObjectValue builds the JSON Schema `additionalProperties`
// descriptor for an object field. Priority order:
//
//  1. providerOptions (domains_create / domains_update): return nil so
//     the constraint is dropped entirely and booleans round-trip
//     natively. (Existing carve-out, preserved.)
//  2. Manifest declares `valueType` (and optionally `valueFields` for
//     object values): emit the rich schema so map values are typed.
//  3. requires (apps/add / apps/update / deploy): hardcoded fallback
//     for the map[alias] → {id, type, config, env} shape until the
//     conductor manifest grows valueType/valueFields for it. Closes
//     I26-N. Removable once the manifest entry ships.
//  4. Default: legacy `{type: "string"}` for back-compat with every
//     map[string]string field whose manifest entry pre-dates
//     valueType.
func (s *Server) projectObjectValue(field manifest.Field) *Property {
	if field.Name == "providerOptions" {
		return nil
	}
	if field.ValueType != "" {
		mapped := s.mapType(field.ValueType)
		value := &Property{Type: mapped}
		if mapped == "object" && len(field.ValueFields) > 0 {
			value.Properties = make(map[string]Property, len(field.ValueFields))
			for _, sub := range field.ValueFields {
				subProp := Property{
					Type:        s.mapType(sub.Type),
					Description: sub.Description,
				}
				if len(sub.Enum) > 0 {
					subProp.Enum = sub.Enum
				}
				if sub.Default != nil {
					subProp.Default = sub.Default
				}
				value.Properties[sub.Name] = subProp
				if sub.Required {
					value.Required = append(value.Required, sub.Name)
				}
			}
		}
		return value
	}
	if field.Name == "requires" {
		// Hardcoded transition-window fallback for I26-N: map[alias] →
		// {id, type, config?, env?}. Conductor's manifest doesn't carry
		// valueType/valueFields for `requires` yet; this entry can be
		// dropped once it does (the `ValueType != ""` branch above
		// will take over automatically).
		return &Property{
			Type: "object",
			Properties: map[string]Property{
				"id":   {Type: "string", Description: "The 5-char osid of an existing service to link to. Required."},
				"type": {Type: "string", Description: "Service type slug (postgresql, valkey, mysql, ...). Required; must match the linked service's type."},
				"config": {
					Type:                 "object",
					AdditionalProperties: &Property{Type: "string"},
					Description:          "Optional config-map keyed env vars injected into the app pod (read from the service's stored config; e.g. DATABASE → DATABASE_NAME).",
				},
				"env": {
					Type:                 "object",
					AdditionalProperties: &Property{Type: "string"},
					Description:          "Optional secret-map keyed env vars injected into the app pod (read from the service's generated secrets; e.g. URL → DATABASE_URL).",
				},
			},
			Required: []string{"id", "type"},
		}
	}
	return &Property{Type: "string"}
}

// projectArrayItems builds the JSON Schema `items` descriptor for an
// array field. When the manifest declares `itemType` (and optionally
// `itemFields` for object elements), the projection emits the richer
// shape so LLMs that strict-validate the tool schema send the correct
// element type. Defaults to `{type: "string"}` for back-compat with
// existing []string fields whose manifest entries pre-date itemType.
//
// Regression target: I6-H. The conductor's R2 manifest bump added
// itemType + itemFields to apps/secret-files/update.add and
// apps/secrets/update.add but the projection bridge here kept emitting
// items.type=string, so MCP clients that followed the schema strictly
// still hit "Each file in 'add' must have a 'filename' string" on the
// server.
func (s *Server) projectArrayItems(field manifest.Field) *Property {
	itemType := field.ItemType
	if itemType == "" {
		return &Property{Type: "string"}
	}
	mapped := s.mapType(itemType)
	items := &Property{Type: mapped}
	if mapped == "object" && len(field.ItemFields) > 0 {
		items.Properties = make(map[string]Property, len(field.ItemFields))
		for _, sub := range field.ItemFields {
			subProp := Property{
				Type:        s.mapType(sub.Type),
				Description: sub.Description,
			}
			if len(sub.Enum) > 0 {
				subProp.Enum = sub.Enum
			}
			if sub.Default != nil {
				subProp.Default = sub.Default
			}
			items.Properties[sub.Name] = subProp
			if sub.Required {
				items.Required = append(items.Required, sub.Name)
			}
		}
	}
	return items
}

func (s *Server) sendResponse(resp *Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MCP: failed to marshal response: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

// sendNotification writes a JSON-RPC notification: a method call with no
// id, which the client must not answer. Used to tell the client the tool
// list changed after a manifest refresh (B2).
func (s *Server) sendNotification(method string) {
	data, err := json.Marshal(Notification{JSONRPC: "2.0", Method: method})
	if err != nil {
		fmt.Fprintf(os.Stderr, "MCP: failed to marshal notification: %v\n", err)
		return
	}
	fmt.Println(string(data))
}

func (s *Server) sendError(id any, code int, message, data string) {
	resp := &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
	s.sendResponse(resp)
}
