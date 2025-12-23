package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"cli/internal/manifest"
)

// JSON-RPC types
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *Error      `json:"error,omitempty"`
}

type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MCP types
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
}

type Capabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema InputSchema `json:"inputSchema"`
}

type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Server is the MCP server
type Server struct {
	manifest *manifest.Manifest
	executor ToolExecutor
	version  string
}

// ToolExecutor executes tools
type ToolExecutor interface {
	Execute(toolName string, args map[string]interface{}) (string, error)
	ExecuteRaw(method, endpoint string, body map[string]interface{}, cid string) (string, error)
}

// NewServer creates a new MCP server
func NewServer(m *manifest.Manifest, executor ToolExecutor, version string) *Server {
	return &Server{
		manifest: m,
		executor: executor,
		version:  version,
	}
}

// Run starts the MCP server on stdio
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
	case "initialized":
		// Notification, no response needed
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "ping":
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{},
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

func (s *Server) handleInitialize(req *Request) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: Capabilities{
				Tools: &ToolsCapability{},
			},
			ServerInfo: ServerInfo{
				Name:    "runos",
				Version: s.version,
			},
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

	// Handle built-in api_request tool
	if params.Name == "api_request" {
		result, err = s.handleAPIRequest(params.Arguments)
	} else {
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

func (s *Server) handleAPIRequest(args map[string]interface{}) (string, error) {
	method, ok := args["method"].(string)
	if !ok || method == "" {
		return "", fmt.Errorf("method is required")
	}

	endpoint, ok := args["endpoint"].(string)
	if !ok || endpoint == "" {
		return "", fmt.Errorf("endpoint is required")
	}

	cid, _ := args["cid"].(string)

	var body map[string]interface{}
	if b, ok := args["body"].(map[string]interface{}); ok {
		body = b
	}

	return s.executor.ExecuteRaw(method, endpoint, body, cid)
}

func (s *Server) buildTools() []Tool {
	var tools []Tool

	// Built-in api_request tool for arbitrary API calls
	tools = append(tools, Tool{
		Name:        "api_request",
		Description: "Make an arbitrary HTTP request to the RunOS API. Use this to test endpoints, debug API calls, or make requests not covered by other tools. Returns status code and response body.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"method": {
					Type:        "string",
					Description: "HTTP method (GET, POST, PUT, PATCH, DELETE)",
					Enum:        []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
				},
				"endpoint": {
					Type:        "string",
					Description: "API endpoint path (e.g., /api/backend/v1/osi/instance/valkey-abc123)",
				},
				"body": {
					Type:        "object",
					Description: "Request body as JSON object (for POST/PUT/PATCH requests)",
				},
				"cid": {
					Type:        "string",
					Description: "Cluster ID for the X-CID header (required for most API calls)",
				},
			},
			Required: []string{"method", "endpoint", "cid"},
		},
	})

	for _, cmd := range s.manifest.Commands {
		tool := Tool{
			Name:        strings.ReplaceAll(cmd.Command, "/", "_"),
			Description: cmd.Description,
			InputSchema: InputSchema{
				Type:       "object",
				Properties: make(map[string]Property),
			},
		}

		if cmd.Input != nil {
			var required []string

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
					required = append(required, field.Name)
				}
			}

			for _, flag := range cmd.Input.Flags {
				tool.InputSchema.Properties[flag.Name] = Property{
					Type:        "boolean",
					Description: flag.Description,
					Default:     flag.Default,
				}
			}

			tool.InputSchema.Required = required
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
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

func (s *Server) sendError(id interface{}, code int, message, data string) {
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
