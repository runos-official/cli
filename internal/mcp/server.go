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
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// minTopicsRead is the number of distinct documentation topics the LLM must
// consume (via mcp_topics_search or mcp_topics_show) after bootstrap before
// other tools become available on the read server.
const minTopicsRead = 3

// Server instructions by category
var serverInstructions = map[string]string{
	"read": `RunOS MCP Server (Read-Only)

Query clusters, services, apps, and infrastructure state. No modifications.

REQUIRED FIRST STEPS (in order):
1. Call mcp_bootstrap to receive critical instructions and the topic index.
2. Read documentation for at least 3 distinct topics relevant to the user's task using mcp_topics_search (preferred, by keywords) or mcp_topics_show (by exact key). Other tools are blocked until this is satisfied.

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
	// topicsRead is the set of distinct topic keys the LLM has consumed via
	// mcp_topics_search or mcp_topics_show during this session. Used to gate
	// non-topic tools on the read server until minTopicsRead is reached.
	topicsRead map[string]struct{}
}

// ToolExecutor defines the interface for executing MCP tools against the API.
type ToolExecutor interface {
	Execute(toolName string, args map[string]any) (string, error)
	ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error)
}

// NewServer creates a new MCP server with the given manifest, executor, version, and category.
func NewServer(m *manifest.Manifest, executor ToolExecutor, version, category string) *Server {
	return &Server{
		manifest: m,
		executor: executor,
		version:  version,
		category: category,
	}
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
				Tools: &ToolsCapability{},
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
	tools := s.buildTools()
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

	// Bootstrap gate: handle mcp_bootstrap specially and enforce bootstrap-first on read server
	if params.Name == "mcp_bootstrap" {
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
		s.bootstrapped = true
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: result}},
			},
		}
	}

	if !s.bootstrapped && s.category == "read" {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: "ERROR: You must call the mcp_bootstrap tool before using any other tools. Call mcp_bootstrap now (no arguments needed) to receive critical instructions for correct RunOS usage."}},
				IsError: true,
			},
		}
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
		s.recordTopicsRead(params.Name, params.Arguments, result)
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentBlock{{Type: "text", Text: result}},
			},
		}
	}

	// Topic-read gate: on the read server, require minTopicsRead distinct topic
	// keys to have been consumed before any non-topic, non-bootstrap tool runs.
	if s.category == "read" && len(s.topicsRead) < minTopicsRead {
		msg := fmt.Sprintf(
			"ERROR: You have read %d/%d required topics. Before using other tools, read documentation for at least %d distinct topics relevant to the user's task. Use mcp_topics_search with keywords (e.g. \"deploy\", \"postgresql\", \"dockerfile\"), or mcp_topics_show with an exact key from the bootstrap topic index. This ensures you follow correct RunOS procedures instead of guessing.",
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
	} else if isStaticAppsTool(params.Name) {
		// Static apps subcommands (pull, diff, sync, list-previous-uploads)
		// are not in the manifest; they orchestrate local filesystem
		// alongside API calls and run as runos subprocesses.
		result, err = s.handleAppsCommand(params.Name, params.Arguments)
	} else if isStaticServicesTool(params.Name) {
		// Static services subcommands (pull, diff, sync) follow the same
		// pattern: yaml-file driven, manifest-aware, run via subprocess.
		result, err = s.handleServicesCommand(params.Name, params.Arguments)
	} else {
		// Auto-inject CLI version and OS for version check tool
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

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: CallToolResult{
			Content: []ContentBlock{{Type: "text", Text: result}},
		},
	}
}

// recordTopicsRead records the distinct topic keys consumed by a successful
// mcp_topics_search or mcp_topics_show call. For search, it parses topic keys
// out of the response. For show, it uses the requested key argument.
func (s *Server) recordTopicsRead(toolName string, args map[string]any, result string) {
	if s.topicsRead == nil {
		s.topicsRead = make(map[string]struct{})
	}
	switch toolName {
	case "mcp_topics_show":
		if k, ok := args["key"].(string); ok && k != "" {
			s.topicsRead[k] = struct{}{}
		}
	case "mcp_topics_search":
		var resp map[string]any
		if err := json.Unmarshal([]byte(result), &resp); err != nil {
			return
		}
		topics, ok := resp["topics"].([]any)
		if !ok {
			return
		}
		for _, item := range topics {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if k, ok := m["key"].(string); ok && k != "" {
				s.topicsRead[k] = struct{}{}
			}
		}
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

func (s *Server) buildTools() []Tool {
	var tools []Tool

	for _, cmd := range s.manifest.Commands {
		// Skip if command doesn't belong to this MCP server category
		if !contains(cmd.MCP, s.category) {
			continue
		}

		// Tool name = command path with {id} placeholders stripped, then / replaced by _
		// e.g., "services/minio/{id}/show" -> "services/minio/show" -> "services_minio_show"
		cmdPath := placeholderRegex.ReplaceAllString(cmd.Command, "")
		toolName := strings.ReplaceAll(cmdPath, "/", "_")

		// Skip deploy - it has a built-in handler
		if toolName == "deploy" {
			// Add deploy tool with custom description and cid parameter
			tools = append(tools, Tool{
				Name: "deploy",
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

VCS deploys with an unchanged sha are near-instant: the orchestration short-circuits the build when the image is already in Harbor and only runs manifest reconcile + rollout watch. Use this to apply config drift after editing the yaml without a rebuild: edit yaml, commit, push, deploy with the new sha.`,
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]Property{
						"cid": {
							Type:        "string",
							Description: "Cluster ID to deploy to, in format 'xyz (Cluster Name)' e.g. 'mycluster2 (Local AI Cluster)'. REQUIRED if no default cluster is set. Get from user or use clusters_list.",
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
		}

		// Add cid parameter if endpoint requires it
		if strings.Contains(cmd.Endpoint, ":cid") {
			tool.InputSchema.Properties["cid"] = Property{
				Type:        "string",
				Description: "Cluster ID in format 'xyz (Cluster Name)' e.g. 'mycluster2 (Local AI Cluster)'. Always include the name so user sees which cluster is being used. Get from user or use clusters_list.",
			}
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
				// container. The manifest doesn't carry richer types yet, so
				// we default to string values — every map[string]string and
				// []string we currently expose works under that schema, and
				// LLM libraries that strict-validate the schema (e.g. some
				// versions of the Anthropic SDK's tool wrapper) accept it.
				if prop.Type == "object" {
					prop.AdditionalProperties = &Property{Type: "string"}
				}
				if prop.Type == "array" {
					prop.Items = &Property{Type: "string"}
				}
				tool.InputSchema.Properties[field.Name] = prop

				if field.Required {
					tool.InputSchema.Required = append(tool.InputSchema.Required, field.Name)
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

func (s *Server) sendResponse(resp *Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MCP: failed to marshal response: %v\n", err)
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
