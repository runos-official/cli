package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// cmdPlaceholderRegex matches {name} patterns in command paths
var cmdPlaceholderRegex = regexp.MustCompile(`/?\{[^}]+\}`)

// CommandExecutor executes manifest commands by making authenticated HTTP requests to the API.
type CommandExecutor struct {
	manifest   *manifest.Manifest
	baseURL    string
	httpClient *http.Client
}

// NewCommandExecutor creates a new CommandExecutor for the given manifest and base URL.
func NewCommandExecutor(m *manifest.Manifest, baseURL string) *CommandExecutor {
	return &CommandExecutor{
		manifest: m,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExecuteRaw makes an arbitrary authenticated API request with the given method, endpoint, body, and cluster ID.
func (e *CommandExecutor) ExecuteRaw(method, endpoint string, body map[string]any, cid string) (string, error) {
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

	// Read response (limit to 10 MB)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Build result with status info
	result := map[string]any{
		"status":      resp.StatusCode,
		"status_text": resp.Status,
	}

	// Try to parse response as JSON
	var jsonResp any
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

// Execute runs a tool by name, mapping it to the corresponding manifest command and making the API request.
func (e *CommandExecutor) Execute(toolName string, args map[string]any) (string, error) {
	// Convert tool name back to command path
	cmdPath := strings.ReplaceAll(toolName, "_", "/")

	// Find the command - need to match against command paths with {id} placeholders stripped
	var cmdDef *manifest.Command
	for _, cmd := range e.manifest.Commands {
		// Strip placeholders from manifest command for comparison
		// e.g., "services/minio/{id}/show" -> "services/minio/show"
		manifestCmdPath := cmdPlaceholderRegex.ReplaceAllString(cmd.Command, "")
		if manifestCmdPath == cmdPath {
			cmdDef = &cmd
			break
		}
	}

	if cmdDef == nil {
		return "", fmt.Errorf("unknown command: %s", toolName)
	}

	// Extract cid from args if provided (will be used in endpoint building)
	// cid can be in format "xyz" or "xyz (Cluster Name)" - extract short ID before space
	cid, _ := args["cid"].(string)
	cid = extractCID(cid)

	// Get auth token
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

	token, err := e.getAuthToken(cfg)
	if err != nil {
		return "", fmt.Errorf("authentication required: run 'runos login' first")
	}

	// Build endpoint URL with explicit cid if provided
	endpoint, err := e.buildEndpointWithCID(cmdDef.Endpoint, args, cmdDef, cid)
	if err != nil {
		return "", err
	}

	// Build request body (for POST/PUT/PATCH) - exclude cid from body
	body := e.buildBody(args, cmdDef)

	// Make request
	resp, err := e.doRequest(cmdDef.Method, endpoint, body, token)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response (limit to 10 MB)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	// Pretty print JSON response
	var jsonResp any
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
	if cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}
	return auth.GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

func (e *CommandExecutor) buildEndpointWithCID(endpoint string, args map[string]any, cmdDef *manifest.Command, cid string) (string, error) {
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
		result = strings.ReplaceAll(result, ":aid", url.PathEscape(cfg.AccountID))
	}

	// Substitute :cid with cluster ID - use passed cid if provided, otherwise use default
	if strings.Contains(result, ":cid") {
		if cid == "" {
			cid = cfg.GetDefaultClusterID()
		}
		if cid == "" {
			return "", fmt.Errorf("cluster ID required: pass cid parameter or set default with 'runos config set cid <cluster-id>'")
		}
		result = strings.ReplaceAll(result, ":cid", url.PathEscape(cid))
	}

	// Substitute field placeholders from args (both positional and non-positional)
	// With the new manifest structure, fields like "id" are no longer positional
	// but still need to be substituted in the endpoint path.
	// AI models may prefix generic field names (e.g. sending "app_id" instead of "id"),
	// so we fall back to matching the URL-path entity as a prefix.
	if cmdDef.Input != nil {
		for _, field := range cmdDef.Input.Fields {
			val, ok := args[field.Name]
			if !ok {
				// Derive the expected prefix from the endpoint path segment before the placeholder.
				// e.g. endpoint "/:aid/:cid/apps/:id/status" → segment before ":id" is "apps" → try "apps_id", "app_id"
				placeholder := ":" + field.Name
				if idx := strings.Index(result, placeholder); idx > 0 {
					prefix := result[:idx]
					parts := strings.Split(strings.Trim(prefix, "/"), "/")
					if len(parts) > 0 {
						entity := parts[len(parts)-1]
						if v, found := args[entity+"_"+field.Name]; found {
							val = v
							ok = true
						} else if singular := strings.TrimSuffix(entity, "s"); singular != entity {
							if v, found := args[singular+"_"+field.Name]; found {
								val = v
								ok = true
							}
						}
					}
				}
			}
			if ok {
				valStr := fmt.Sprintf("%v", val)
				// Handle both placeholder styles: {name} and :name (URL-encode for safety)
				escapedVal := url.PathEscape(valStr)
				result = strings.ReplaceAll(result, "{"+field.Name+"}", escapedVal)
				result = strings.ReplaceAll(result, ":"+field.Name, escapedVal)
			}
		}
	}

	// For GET and DELETE requests, append non-positional fields as query parameters
	if (cmdDef.Method == http.MethodGet || cmdDef.Method == http.MethodDelete) && cmdDef.Input != nil {
		queryParams := url.Values{}
		for _, field := range cmdDef.Input.Fields {
			// Skip positional fields (already in URL path) and cid (handled separately)
			if field.Positional || field.Name == "cid" {
				continue
			}
			if val, ok := args[field.Name]; ok {
				queryParams.Set(field.Name, fmt.Sprintf("%v", val))
			} else if field.Default != nil {
				queryParams.Set(field.Name, fmt.Sprintf("%v", field.Default))
			}
		}
		// For DELETE, also include flags as query parameters
		if cmdDef.Method == http.MethodDelete {
			for _, flag := range cmdDef.Input.Flags {
				if val, ok := args[flag.Name]; ok {
					queryParams.Set(flag.Name, fmt.Sprintf("%v", val))
				}
			}
		}
		if len(queryParams) > 0 {
			result = result + "?" + queryParams.Encode()
		}
	}

	return e.baseURL + result, nil
}

func (e *CommandExecutor) buildBody(args map[string]any, cmdDef *manifest.Command) map[string]any {
	if cmdDef.Method != http.MethodPost && cmdDef.Method != http.MethodPut && cmdDef.Method != http.MethodPatch {
		return nil
	}

	body := make(map[string]any)

	if cmdDef.Input == nil {
		return body
	}

	// Add field values (excluding fields that go in URL path)
	for _, field := range cmdDef.Input.Fields {
		// Skip positional fields (they go in URL)
		if field.Positional {
			continue
		}
		// Skip fields that appear in the endpoint path as :fieldName or {fieldName}
		// e.g., endpoint "/:aid/:cid/services/minio/:id" means id should not be in body
		if strings.Contains(cmdDef.Endpoint, ":"+field.Name) || strings.Contains(cmdDef.Endpoint, "{"+field.Name+"}") {
			continue
		}
		if val, ok := args[field.Name]; ok {
			body[field.Name] = val
		} else if field.Default != nil {
			body[field.Name] = field.Default
		}
	}

	// Add flag values nested inside a "flags" object
	if len(cmdDef.Input.Flags) > 0 {
		flagsObj := make(map[string]any)
		for _, flag := range cmdDef.Input.Flags {
			if val, ok := args[flag.Name]; ok {
				flagsObj[flag.Name] = val
			} else {
				flagsObj[flag.Name] = flag.Default
			}
		}
		if len(flagsObj) > 0 {
			body["flags"] = flagsObj
		}
	}

	return body
}

func (e *CommandExecutor) doRequest(method, url string, body map[string]any, token string) (*http.Response, error) {
	return e.doRequestWithCID(method, url, body, token, "")
}

// unflattenBody converts dot-notation keys into nested objects.
// e.g., {"providerConfig.location": "hel1"} becomes {"providerConfig": {"location": "hel1"}}
func unflattenBody(body map[string]any) map[string]any {
	result := make(map[string]any)

	for key, value := range body {
		parts := strings.Split(key, ".")
		if len(parts) == 1 {
			// No dot notation, keep as-is
			result[key] = value
		} else {
			// Navigate/create nested structure
			current := result
			for _, part := range parts[:len(parts)-1] {
				if _, exists := current[part]; !exists {
					current[part] = make(map[string]any)
				}
				// Check if existing value is a map
				if nested, ok := current[part].(map[string]any); ok {
					current = nested
				} else {
					// Conflict: existing value is not a map, create new map
					newMap := make(map[string]any)
					current[part] = newMap
					current = newMap
				}
			}
			// Set the final value
			current[parts[len(parts)-1]] = value
		}
	}

	return result
}

func (e *CommandExecutor) doRequestWithCID(method, url string, body map[string]any, token, cid string) (*http.Response, error) {
	var bodyReader io.Reader

	if len(body) > 0 {
		// Convert dot-notation keys to nested objects
		nestedBody := unflattenBody(body)
		jsonBody, err := json.Marshal(nestedBody)
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

// extractCID extracts the short cluster ID from a string that may be in
// format "xyz (Cluster Name)". Returns the part before the first space.
func extractCID(cid string) string {
	if cid == "" {
		return ""
	}
	if idx := strings.IndexByte(cid, ' '); idx > 0 {
		return cid[:idx]
	}
	return cid
}
