package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/jobs"
	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/output"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Executor executes commands by calling the API
type Executor struct {
	baseURL    string
	httpClient *http.Client
}

// NewExecutor creates a new command executor
func NewExecutor(baseURL string) *Executor {
	return &Executor{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Execute runs the command
func (e *Executor) Execute(cmd *cobra.Command, args []string, cmdDef manifest.Command) error {
	// Get auth token
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	token, err := e.getAuthToken(cfg)
	if err != nil {
		return fmt.Errorf("authentication required: run 'runos login' first (%w)", err)
	}

	// Get cluster ID from flag or config default
	cid, _ := cmd.Flags().GetString("cid")
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}

	// Collect input
	body, err := e.collectInput(cmd, args, cmdDef)
	if err != nil {
		return fmt.Errorf("failed to collect input: %w", err)
	}

	// Build endpoint URL with path parameters substituted
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef, cfg, cid, body)
	if err != nil {
		return err
	}

	// Remove path parameters from body before sending request
	// Fields used in URL path (e.g., :id, :cid) should not be in the request body
	requestBody := filterPathParamsFromBody(body, cmdDef)

	// Make request
	resp, err := e.doRequest(cmdDef.Method, endpoint, requestBody, token)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body (limit to 10 MB)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	// Handle --follow flag for commands that return jobs (detected by jobId in output)
	if hasJobIdOutput(cmdDef) {
		follow, _ := cmd.Flags().GetBool("follow")
		if follow {
			return e.followJob(respBody)
		}
	}

	// Format and display output
	jsonOutput, _ := cmd.Flags().GetBool("json")

	// Add default indicator for clusters/list (only for plain text output)
	if cmdDef.Command == "clusters/list" && !jsonOutput {
		respBody = markDefaultCluster(respBody, cfg.DefaultClusterID)
	}
	formatter := output.NewFormatter(jsonOutput)

	err = formatter.Format(respBody, cmdDef.Output)
	if err != nil {
		return err
	}

	// Print footer note for clusters/list (only in plain text mode to a terminal)
	if cmdDef.Command == "clusters/list" && !jsonOutput && cfg.DefaultClusterID != "" && term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println()
		fmt.Println("* default cluster - commands use this cluster if --cid is not specified")
	}

	return nil
}

func (e *Executor) followJob(respBody []byte) error {
	// Extract jobId from response
	var response map[string]any
	if err := json.Unmarshal(respBody, &response); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	jobID, ok := response["jobId"].(string)
	if !ok {
		return fmt.Errorf("response does not contain jobId")
	}

	return jobs.FollowJob(jobID)
}

func (e *Executor) getAuthToken(cfg *config.Config) (string, error) {
	if cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}
	return auth.GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

func (e *Executor) collectInput(cmd *cobra.Command, args []string, cmdDef manifest.Command) (map[string]any, error) {
	result := make(map[string]any)

	if cmdDef.Input == nil {
		return result, nil
	}

	// 1. Apply defaults
	for _, field := range cmdDef.Input.Fields {
		if field.Default != nil && !field.Positional {
			result[field.Name] = field.Default
		}
	}
	for _, flag := range cmdDef.Input.Flags {
		result[flag.Name] = flag.Default
	}

	// 2. Load from file if -f provided
	filePath, _ := cmd.Flags().GetString("file")
	if filePath != "" {
		fileData, err := loadYAMLFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to load file: %w", err)
		}
		for k, v := range fileData {
			result[k] = v
		}
	}

	// 3. Override with flags
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			continue
		}

		if cmd.Flags().Changed(field.Name) {
			switch field.Type {
			case "string":
				val, _ := cmd.Flags().GetString(field.Name)
				result[field.Name] = val
			case "integer":
				val, _ := cmd.Flags().GetInt(field.Name)
				result[field.Name] = val
			case "array":
				val, _ := cmd.Flags().GetStringSlice(field.Name)
				if field.Format == "key_value" {
					result[field.Name] = parseKeyValueTags(val)
				} else {
					result[field.Name] = val
				}
			case "boolean":
				val, _ := cmd.Flags().GetBool(field.Name)
				result[field.Name] = val
			}
		}
	}

	// Override boolean flags (from flags array, separate from fields)
	for _, flag := range cmdDef.Input.Flags {
		if cmd.Flags().Changed(flag.Name) {
			val, _ := cmd.Flags().GetBool(flag.Name)
			result[flag.Name] = val
		}
	}

	return result, nil
}

func (e *Executor) buildEndpoint(endpoint string, args []string, cmdDef manifest.Command, cfg *config.Config, cid string, body map[string]any) (string, error) {
	result := endpoint

	// Substitute :aid with account ID from config
	if strings.Contains(result, ":aid") {
		if cfg.AccountID == "" {
			return "", fmt.Errorf("account ID not set: run 'runos login' first")
		}
		result = strings.ReplaceAll(result, ":aid", url.PathEscape(cfg.AccountID))
	}

	// Substitute :cid with cluster ID
	if strings.Contains(result, ":cid") {
		if cid == "" {
			return "", fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
		}
		result = strings.ReplaceAll(result, ":cid", url.PathEscape(cid))
	}

	// Substitute field placeholders from body (flags) and positional args
	if cmdDef.Input != nil {
		argIndex := 0
		for _, field := range cmdDef.Input.Fields {
			var value string

			if field.Positional && argIndex < len(args) {
				// Get value from positional arg
				value = args[argIndex]
				argIndex++
			} else if val, ok := body[field.Name]; ok {
				// Get value from body (flag input)
				value = fmt.Sprintf("%v", val)
			}

			if value != "" {
				// Substitute in endpoint path (URL-encode for safety)
				escapedValue := url.PathEscape(value)
				result = strings.ReplaceAll(result, "{"+field.Name+"}", escapedValue)
				result = strings.ReplaceAll(result, ":"+field.Name, escapedValue)
			}
		}
	}

	// For GET and DELETE requests, append non-positional fields as query parameters
	if (cmdDef.Method == http.MethodGet || cmdDef.Method == http.MethodDelete) && cmdDef.Input != nil {
		queryParams := url.Values{}
		for _, field := range cmdDef.Input.Fields {
			// Skip positional fields (already in URL path)
			if field.Positional {
				continue
			}
			if val, ok := body[field.Name]; ok {
				queryParams.Set(field.Name, fmt.Sprintf("%v", val))
			}
		}
		// For DELETE, also include flags as query parameters
		if cmdDef.Method == http.MethodDelete {
			for _, flag := range cmdDef.Input.Flags {
				if val, ok := body[flag.Name]; ok {
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

// filterPathParamsFromBody removes fields that are used in the URL path from the request body,
// and nests flag values inside a "flags" object.
// Fields like "id" that appear as :id in the endpoint should not be sent in the body.
func filterPathParamsFromBody(body map[string]any, cmdDef manifest.Command) map[string]any {
	if cmdDef.Input == nil {
		return body
	}

	result := make(map[string]any)
	flagsObj := make(map[string]any)

	// Build a set of flag names for quick lookup
	flagNames := make(map[string]bool)
	for _, flag := range cmdDef.Input.Flags {
		flagNames[flag.Name] = true
	}

	for key, value := range body {
		// Skip if this field appears in the endpoint path as :fieldName or {fieldName}
		if strings.Contains(cmdDef.Endpoint, ":"+key) || strings.Contains(cmdDef.Endpoint, "{"+key+"}") {
			continue
		}
		// If it's a flag, add to flags object
		if flagNames[key] {
			flagsObj[key] = value
		} else {
			result[key] = value
		}
	}

	// Add flags object if there are any flags
	if len(flagsObj) > 0 {
		result["flags"] = flagsObj
	}

	return result
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

func (e *Executor) doRequest(method, url string, body map[string]any, token string) (*http.Response, error) {
	var bodyReader io.Reader

	if len(body) > 0 && (method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch) {
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
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return e.httpClient.Do(req)
}

func loadYAMLFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return result, nil
}

func parseKeyValueTags(tags []string) []map[string]string {
	result := make([]map[string]string, 0, len(tags))
	for _, tag := range tags {
		parts := strings.SplitN(tag, ":", 2)
		if len(parts) == 2 {
			result = append(result, map[string]string{
				"key":   parts[0],
				"value": parts[1],
			})
		} else {
			result = append(result, map[string]string{
				"key": parts[0],
			})
		}
	}
	return result
}

func markDefaultCluster(data []byte, defaultCID string) []byte {
	if defaultCID == "" {
		return data
	}

	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return data
	}

	for _, item := range items {
		cid, ok := item["cid"].(string)
		if ok && cid == defaultCID {
			item["cid"] = cid + "*"
		}
	}

	result, err := json.Marshal(items)
	if err != nil {
		return data
	}
	return result
}
