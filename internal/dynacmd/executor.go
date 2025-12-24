package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cli/internal/auth"
	"cli/internal/config"
	"cli/internal/manifest"
	"cli/internal/output"

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
		return fmt.Errorf("authentication required: run 'runos login' first")
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
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef, cfg, cid)
	if err != nil {
		return err
	}

	// Make request
	resp, err := e.doRequest(cmdDef.Method, endpoint, body, token)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Check for errors
	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBody))
	}

	// Format and display output
	jsonOutput, _ := cmd.Flags().GetBool("json")
	formatter := output.NewFormatter(jsonOutput)

	return formatter.Format(respBody, cmdDef.Output)
}

func (e *Executor) getAuthToken(cfg *config.Config) (string, error) {
	if cfg.RefreshToken == "" || cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}

	refreshResp, err := auth.RefreshIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
	if err != nil {
		return "", err
	}

	return refreshResp.IDToken, nil
}

func (e *Executor) collectInput(cmd *cobra.Command, args []string, cmdDef manifest.Command) (map[string]interface{}, error) {
	result := make(map[string]interface{})

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
			}
		}
	}

	// Override boolean flags
	for _, flag := range cmdDef.Input.Flags {
		if cmd.Flags().Changed(flag.Name) {
			val, _ := cmd.Flags().GetBool(flag.Name)
			result[flag.Name] = val
		}
	}

	return result, nil
}

func (e *Executor) buildEndpoint(endpoint string, args []string, cmdDef manifest.Command, cfg *config.Config, cid string) (string, error) {
	result := endpoint

	// Substitute :aid with account ID from config
	if strings.Contains(result, ":aid") {
		if cfg.AccountID == "" {
			return "", fmt.Errorf("account ID not set: run 'runos login' first")
		}
		result = strings.Replace(result, ":aid", cfg.AccountID, -1)
	}

	// Substitute :cid with cluster ID
	if strings.Contains(result, ":cid") {
		if cid == "" {
			return "", fmt.Errorf("cluster ID required: use --cid flag or set default with 'runos config set cid <cluster-id>'")
		}
		result = strings.Replace(result, ":cid", cid, -1)
	}

	// Substitute field placeholders from input
	if cmdDef.Input != nil {
		argIndex := 0
		for _, field := range cmdDef.Input.Fields {
			if field.Positional && argIndex < len(args) {
				value := args[argIndex]
				argIndex++

				// Handle both placeholder styles: {name} and :name
				result = strings.Replace(result, "{"+field.Name+"}", value, -1)
				result = strings.Replace(result, ":"+field.Name, value, -1)
			}
		}
	}

	return e.baseURL + result, nil
}

func (e *Executor) doRequest(method, url string, body map[string]interface{}, token string) (*http.Response, error) {
	var bodyReader io.Reader

	if len(body) > 0 && (method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch) {
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
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return e.httpClient.Do(req)
}

func loadYAMLFile(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
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
