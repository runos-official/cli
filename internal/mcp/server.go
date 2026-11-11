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
	"strings"
	"time"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

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
