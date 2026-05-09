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

// APIError is returned by ExecuteWithInput (and the shared dispatch path)
// when the conductor responds with a non-2xx status. Callers can errors.As
// it to format the body specially (e.g. format the dependents list out of a
// 409 from services delete).
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error renders the same one-liner the historic Execute path emitted, so
// behaviour is unchanged for callers that don't unwrap.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, string(e.Body))
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

	// Auto-inject CLI version + OS for `cli/version-check`. The MCP wrapper
	// already does this so the answer is correct under MCP; the bare CLI
	// path used to send empty version, which produced a misleading
	// `updateAvailable: true` and an alarming `releaseNotes` sentinel.
	// Injection mirrors the MCP wrapper's behaviour at internal/mcp/server.go.
	if cmdDef.Command == "cli/version-check" {
		if _, ok := body["version"]; !ok || isEmptyString(body["version"]) {
			body["version"] = cliRuntimeVersion()
		}
		if _, ok := body["os"]; !ok || isEmptyString(body["os"]) {
			body["os"] = cliRuntimeOS()
		}
	}

	respBody, err := e.dispatch(cmdDef, args, body, cid, cfg, token)
	if err != nil {
		// Conductor's services delete returns 409 with a structured
		// dependents list when other apps/services reference the
		// target. Render it as a multi-line message so the user
		// (and any LLM running this) immediately sees what's blocking
		// the delete instead of a JSON dump in the default
		// APIError.Error() form.
		if msg, ok := formatDependentsError(err); ok {
			return fmt.Errorf("%s", msg)
		}
		return err
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

// ExecuteWithInput drives a manifest command without going through cobra
// flag parsing. Used by static commands (e.g. services_pull / services_diff
// / services_sync) that already have their input as a typed map. Returns
// the raw response body on 2xx; on non-2xx, returns an *APIError that
// carries the status code and the raw body so callers can format it (e.g.
// 409 dependents list out of services delete).
//
// positionalArgs feeds the same buildEndpoint path that Execute uses, so
// fields marked positional in the manifest are substituted into the URL.
// input contains every non-positional value the command needs (PATCH/POST
// body fields, GET/DELETE query parameters); the dispatch path filters out
// keys that double as path parameters.
//
// cid empty falls back to the default cluster id from config, matching
// Execute's "no --cid means use default" behaviour.
func (e *Executor) ExecuteWithInput(cmdDef manifest.Command, positionalArgs []string, input map[string]any, cid string) ([]byte, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	token, err := e.getAuthToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' first (%w)", err)
	}
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	return e.dispatch(cmdDef, positionalArgs, input, cid, cfg, token)
}

// formatDependentsError checks whether err is an *APIError carrying a
// 409 with a structured dependents body (the shape conductor's services
// delete handler returns when other apps/services reference the target).
// Returns a friendly multi-line rendering plus true; (_, false) for any
// other error so callers can fall back to the default formatting.
//
// This is a generic helper; nothing about it is services-specific. Any
// future endpoint that surfaces a 409+dependents body gets the same
// treatment automatically.
func formatDependentsError(err error) (string, bool) {
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != http.StatusConflict {
		return "", false
	}
	var body struct {
		Error      string `json:"error"`
		Dependents []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Alias string `json:"alias"`
		} `json:"dependents"`
	}
	if err := json.Unmarshal(apiErr.Body, &body); err != nil {
		return "", false
	}
	if len(body.Dependents) == 0 {
		return "", false
	}
	var sb strings.Builder
	if body.Error != "" {
		sb.WriteString("refused: ")
		sb.WriteString(body.Error)
		sb.WriteString("\n")
	} else {
		sb.WriteString("refused: this resource has dependents\n")
	}
	sb.WriteString("dependents:\n")
	for _, d := range body.Dependents {
		switch {
		case d.Alias != "" && d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s), alias %q\n", d.Type, d.Name, d.ID, d.Alias))
		case d.Alias != "":
			sb.WriteString(fmt.Sprintf("  - %s (%s), alias %q\n", d.Type, d.ID, d.Alias))
		case d.Name != "":
			sb.WriteString(fmt.Sprintf("  - %s %s (%s)\n", d.Type, d.Name, d.ID))
		default:
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", d.Type, d.ID))
		}
	}
	sb.WriteString("Reconcile each dependent (e.g. update its requires: to point elsewhere, or delete it first) and re-run.")
	return sb.String(), true
}

// dispatch is the shared HTTP path used by both Execute and
// ExecuteWithInput. It builds the endpoint, filters out path-param fields
// from the body, sends the request, and reads the response. Non-2xx
// responses are returned as *APIError so callers can branch on status.
func (e *Executor) dispatch(cmdDef manifest.Command, args []string, body map[string]any, cid string, cfg *config.Config, token string) ([]byte, error) {
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef, cfg, cid, body)
	if err != nil {
		return nil, err
	}
	requestBody := filterPathParamsFromBody(body, cmdDef)
	resp, err := e.doRequest(cmdDef.Method, endpoint, requestBody, token)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: respBody}
	}
	return respBody, nil
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

// getAuthToken resolves the bearer token for outgoing requests. Prefers
// RUNOS_API_KEY when set (CI/CD path); otherwise falls back to the
// Firebase refresh-token exchange that `runos login` set up.
func (e *Executor) getAuthToken(cfg *config.Config) (string, error) {
	return auth.ResolveToken(cfg)
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
		// Positional fields are also exposed as `--<name>` flags so
		// `runos apps status --id y2w1y` works alongside the positional
		// form. Pull the flag value into the body map when set; the
		// endpoint builder still prefers a positional arg when both are
		// present, so the positional path is unchanged.
		if field.Positional {
			if field.Type == "string" && cmd.Flags().Changed(field.Name) {
				val, _ := cmd.Flags().GetString(field.Name)
				result[field.Name] = val
			}
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

	// Substitute :aid with account ID. GetAccountID() prefers the
	// RUNOS_ACCOUNT_ID env var so headless CI runs without a config
	// file's account_id field.
	if strings.Contains(result, ":aid") {
		aid := cfg.GetAccountID()
		if aid == "" {
			return "", fmt.Errorf("account ID not set: run 'runos login' or set RUNOS_ACCOUNT_ID")
		}
		result = strings.ReplaceAll(result, ":aid", url.PathEscape(aid))
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

// isEmptyString reports whether v is a string-typed empty value. Used by
// the cli/version-check auto-injection so a manifest default of "" or a
// flag-default empty string still triggers the runtime fallback.
func isEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s == ""
}
