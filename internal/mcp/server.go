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
	"strings"
	"time"

	"github.com/runos-official/cli/internal/manifest"
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

// Server instructions by category
var serverInstructions = map[string]string{
	"read": `RunOS MCP Server (Read-Only)

Query clusters, services, apps, and infrastructure state. No modifications.

IMPORTANT: Before using any tools, call mcp_bootstrap to learn available topics, service types, and valid parameter values. Do not guess or invent values.`,

	"sensitive_read": `RunOS MCP Server (Sensitive Read)

Access sensitive data like credentials, connection strings, and secrets. Data returned will be visible to the LLM.

IMPORTANT: Before using any tools, call mcp_bootstrap to learn available topics, service types, and valid parameter values. Do not guess or invent values.`,

	"write": `RunOS MCP Server (Write)

Create, update, and manage clusters, services, and applications. Changes affect live infrastructure.

IMPORTANT: Before using any tools, call mcp_bootstrap to learn available topics, service types, and valid parameter values. Do not guess or invent values.`,

	"sensitive_write": `RunOS MCP Server (Sensitive Write)

Perform sensitive write operations like credential rotation and secret management. Changes affect live infrastructure and security-critical data.

IMPORTANT: Before using any tools, call mcp_bootstrap to learn available topics, service types, and valid parameter values. Do not guess or invent values.`,
}

// Server is the MCP server that handles JSON-RPC requests over stdio.
type Server struct {
	manifest *manifest.Manifest
	executor ToolExecutor
	version  string
	category string // "read", "sensitive_read", "write", "sensitive_write"
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

	var result string
	var err error

	// Special handling for deploy (calls runos deploy subprocess)
	if params.Name == "deploy" {
		result, err = s.handleDeploy(params.Arguments)
	} else {
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

func (s *Server) handleDeploy(args map[string]any) (string, error) {
	// Get the runos executable path (same binary that's running this MCP server)
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to find runos executable: %w", err)
	}

	// Build command arguments
	cmdArgs := []string{"deploy", "--follow"}

	// Add cid if provided by the AI - this allows deployment without a default cid set
	// Extract short ID from format "xyz (Cluster Name)"
	if cid, ok := args["cid"].(string); ok && cid != "" {
		cmdArgs = append(cmdArgs, "--cid", extractCID(cid))
	}

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
				Name:        "deploy",
				Description: "Deploy an application from local source. BEFORE CALLING: Check for large files (>1MB) or build artifacts that should be in .dockerignore to avoid slow uploads. Requires runos.yaml and Dockerfile. The Dockerfile MUST run as non-root user.",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]Property{
						"cid": {
							Type:        "string",
							Description: "Cluster ID to deploy to, in format 'xyz (Cluster Name)' e.g. 'mycluster2 (Local AI Cluster)'. REQUIRED if no default cluster is set. Get from user or use clusters_list.",
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

	return tools
}

func (s *Server) mapType(t string) string {
	switch t {
	case "integer":
		return "number"
	case "array":
		return "array"
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
