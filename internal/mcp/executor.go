package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cli/internal/auth"
	"cli/internal/config"
	"cli/internal/manifest"
)

// CommandExecutor executes manifest commands
type CommandExecutor struct {
	manifest   *manifest.Manifest
	baseURL    string
	httpClient *http.Client
}

// NewCommandExecutor creates a new command executor
func NewCommandExecutor(m *manifest.Manifest, baseURL string) *CommandExecutor {
	return &CommandExecutor{
		manifest: m,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExecuteRaw makes an arbitrary API request
func (e *CommandExecutor) ExecuteRaw(method, endpoint string, body map[string]interface{}, cid string) (string, error) {
	// Get auth token
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	token, err := e.getAuthToken(cfg)
	if err != nil {
		return "", fmt.Errorf("authentication required: run 'runos login' first")
	}

	// Build full URL
	url := e.baseURL + endpoint

	// Make request
	resp, err := e.doRequestWithCID(method, url, body, token, cid)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Build result with status info
	result := map[string]interface{}{
		"status":      resp.StatusCode,
		"status_text": resp.Status,
	}

	// Try to parse response as JSON
	var jsonResp interface{}
	if err := json.Unmarshal(respBody, &jsonResp); err != nil {
		result["body"] = string(respBody)
	} else {
		result["body"] = jsonResp
	}

	// Pretty print
	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return string(respBody), nil
	}

	return string(pretty), nil
}

// Execute runs a tool by name
func (e *CommandExecutor) Execute(toolName string, args map[string]interface{}) (string, error) {
	// Convert tool name back to command path
	cmdPath := strings.ReplaceAll(toolName, "_", "/")

	// Find the command
	var cmdDef *manifest.Command
	for _, cmd := range e.manifest.Commands {
		if cmd.Command == cmdPath {
			cmdDef = &cmd
			break
		}
	}

	if cmdDef == nil {
		return "", fmt.Errorf("unknown command: %s", toolName)
	}

	// Get auth token
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	token, err := e.getAuthToken(cfg)
	if err != nil {
		return "", fmt.Errorf("authentication required: run 'runos login' first")
	}

	// Build endpoint URL
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef)
	if err != nil {
		return "", err
	}

	// Build request body (for POST/PUT/PATCH)
	body := e.buildBody(args, cmdDef)

	// Make request
	resp, err := e.doRequest(cmdDef.Method, endpoint, body, token)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	// Pretty print JSON response
	var jsonResp interface{}
	if err := json.Unmarshal(respBody, &jsonResp); err != nil {
		return string(respBody), nil
	}

	pretty, err := json.MarshalIndent(jsonResp, "", "  ")
	if err != nil {
		return string(respBody), nil
	}

	return string(pretty), nil
}

func (e *CommandExecutor) getAuthToken(cfg *config.Config) (string, error) {
	if cfg.RefreshToken == "" || cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}

	refreshResp, err := auth.RefreshIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
	if err != nil {
		return "", err
	}

	return refreshResp.IDToken, nil
}

func (e *CommandExecutor) buildEndpoint(endpoint string, args map[string]interface{}, cmdDef *manifest.Command) (string, error) {
	result := endpoint

	// Load config for account ID and default cluster ID
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	// Substitute :aid with account ID from config
	if strings.Contains(result, ":aid") {
		if cfg.AccountID == "" {
			return "", fmt.Errorf("account ID not set: run 'runos login' first")
		}
		result = strings.ReplaceAll(result, ":aid", cfg.AccountID)
	}

	// Substitute :cid with cluster ID from config
	if strings.Contains(result, ":cid") {
		cid := cfg.GetDefaultClusterID()
		if cid == "" {
			return "", fmt.Errorf("cluster ID required: set default with 'runos config set cid <cluster-id>'")
		}
		result = strings.ReplaceAll(result, ":cid", cid)
	}

	// Substitute positional field placeholders
	if cmdDef.Input != nil {
		for _, field := range cmdDef.Input.Fields {
			if field.Positional {
				if val, ok := args[field.Name]; ok {
					valStr := fmt.Sprintf("%v", val)
					// Handle both placeholder styles: {name} and :name
					result = strings.Replace(result, "{"+field.Name+"}", valStr, -1)
					result = strings.Replace(result, ":"+field.Name, valStr, -1)
				}
			}
		}
	}

	return e.baseURL + result, nil
}

func (e *CommandExecutor) buildBody(args map[string]interface{}, cmdDef *manifest.Command) map[string]interface{} {
	if cmdDef.Method != http.MethodPost && cmdDef.Method != http.MethodPut && cmdDef.Method != http.MethodPatch {
		return nil
	}

	body := make(map[string]interface{})

	if cmdDef.Input == nil {
		return body
	}

	// Add field values (excluding positional args)
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			continue
		}
		if val, ok := args[field.Name]; ok {
			body[field.Name] = val
		} else if field.Default != nil {
			body[field.Name] = field.Default
		}
	}

	// Add flag values
	for _, flag := range cmdDef.Input.Flags {
		if val, ok := args[flag.Name]; ok {
			body[flag.Name] = val
		} else {
			body[flag.Name] = flag.Default
		}
	}

	return body
}

func (e *CommandExecutor) doRequest(method, url string, body map[string]interface{}, token string) (*http.Response, error) {
	return e.doRequestWithCID(method, url, body, token, "")
}

func (e *CommandExecutor) doRequestWithCID(method, url string, body map[string]interface{}, token, cid string) (*http.Response, error) {
	var bodyReader io.Reader

	if len(body) > 0 {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if cid != "" {
		req.Header.Set("X-CID", cid)
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return e.httpClient.Do(req)
}
