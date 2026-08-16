package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/runos-official/cli/internal/apitimeout"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
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
//
// The http.Client carries NO Timeout: each call sets its own deadline via
// apitimeout.For, because a client-wide 30 s cut killed synchronous
// endpoints conductor lets run for up to 600 s (goal 19 A4). MCP had the
// worse half of that bug: an LLM asking for `vms_run-command` with
// timeoutSeconds=120 was told the request failed while the guest command
// ran on to completion.
func NewCommandExecutor(m *manifest.Manifest, baseURL string) *CommandExecutor {
	return &CommandExecutor{
		manifest:   m,
		baseURL:    baseURL,
		httpClient: &http.Client{},
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

	// Make request. ExecuteRaw has no manifest command to derive a
	// deadline from, so it takes the ordinary one.
	ctx, cancel := context.WithTimeout(context.Background(), apitimeout.Default)
	defer cancel()
	resp, err := e.doRequestWithCID(ctx, method, url, body, token, cid)
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

	// Destructive parity with the CLI's --yes (A14). `confirm` is a
	// tool-schema argument, not a manifest field, so it is read here and
	// never reaches the wire: buildBody and the query builder both
	// iterate the manifest's own fields.
	if err := refuseUnconfirmedDestructive(*cmdDef, args, dynacmd.IsDestructiveCommand); err != nil {
		return "", err
	}

	// Extract cid from args if provided (will be used in endpoint building)
	// cid can be in format "xyz" or "xyz (Cluster Name)" - extract short ID before space.
	// Mirror the stripped value back into args so any body-field path that reads
	// args["cid"] (e.g. account-scoped POSTs like cluster-domains/add where cid is a
	// body field, not a path segment) sees the canonical short id, not the display
	// label. Without this, conductor received "mycluster2 (Cluster-2 mycluster2)" as cid in the
	// POST body and failed downstream lookups on the bogus id. Regression: I16-C.
	cid, _ := args["cid"].(string)
	cid = extractCID(cid)
	if _, ok := args["cid"]; ok {
		args["cid"] = cid
	}

	// `cluster-domains show --id runos` targets the synthetic per-cluster
	// runos row, which is ambiguous without a cluster scope (the same id
	// exists once per cluster in the account). The endpoint is global
	// (/:aid/cluster-domains/:id), so even passing cid here can't reach
	// the right scope. The cobra/dynacmd path (internal/dynacmd/executor.go)
	// intercepts the same shape and emits an actionable redirect; mirror
	// it here so MCP/LLM consumers see the same hint instead of a bare
	// conductor 404. Regression target: I17-A (parity with I11-W).
	if cmdDef.Command == "cluster-domains/{id}/show" || cmdDef.Command == "cluster-domains/show" {
		if id, _ := args["id"].(string); id == "runos" {
			return "", fmt.Errorf("`runos` is a synthetic per-cluster cluster-domain; use the `cluster-domains_list-by-cluster` tool with a specific cid to see it scoped to a cluster")
		}
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

	// Build endpoint URL with explicit cid if provided
	endpoint, err := e.buildEndpointWithCID(cmdDef.Endpoint, args, cmdDef, cid)
	if err != nil {
		return "", err
	}

	// I4-K (MCP path): the conductor's PATCH /apps/:id endpoint
	// preserves omitted fields only when `?merge=true` is set on the
	// URL. The cobra/dynacmd path opts in via a similar shim in
	// internal/dynacmd/executor.go:dispatch, but the MCP server runs
	// commands through its own executor (this file), bypassing that
	// shim. Iter-4 R1 retest pinned the diagnosis: cpu/memory
	// preserved (resources fallback runs in both modes) but the 5
	// clearables (healthCheck*, metrics*) wiped, exactly the
	// desired-state-mode signature. Mirror the dynacmd shim here so
	// every CLI surface (cobra, MCP) reaches the conductor with
	// merge semantics for partial-PATCH callers.
	if cmdDef.Command == "apps/update" {
		endpoint = appendMergeQuery(endpoint)
	}

	// Build request body (for POST/PUT/PATCH) - exclude cid from body
	body := e.buildBody(args, cmdDef)

	// Make request under a deadline derived from this command. The cancel
	// stays live until the body has been read below (A4).
	ctx, cancel := context.WithTimeout(context.Background(), apitimeout.For(*cmdDef, body))
	defer cancel()
	resp, err := e.doRequest(ctx, cmdDef.Method, endpoint, body, token)
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
		return "", apiErrorEnvelope(resp.StatusCode, respBody)
	}

	// Empty-body 2xx (typical for DELETE and other no-content endpoints,
	// e.g. `account/api-keys/revoke` returning 200 with no payload).
	// Without this branch the executor would return the empty string and
	// the MCP wrap would emit a content block with no `text` field,
	// failing conformant client validation (foreman #78).
	if isEmptyBody(respBody) {
		return emptyBodySuccessMessage(cmdDef.Command, resp.Status), nil
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

// isEmptyBody reports whether the response body is empty or contains
// only whitespace. Used by the executor to detect no-content 2xx
// responses (foreman #78).
func isEmptyBody(b []byte) bool {
	return len(bytes.TrimSpace(b)) == 0
}

// emptyBodySuccessMessage builds the deterministic success string
// returned by the MCP executor for empty-body 2xx responses, so the
// resulting text content block carries a meaningful payload instead of
// an empty string (foreman #78).
func emptyBodySuccessMessage(command, status string) string {
	if command == "" {
		command = "request"
	}
	if status == "" {
		status = "200 OK"
	}
	return fmt.Sprintf("%s succeeded (%s)", command, status)
}

// getAuthToken resolves the bearer token for outgoing MCP requests via
// auth.ResolveToken, which applies the RUNOS_API_KEY -> stored PAT ->
// Firebase priority order. Previously this hard-required cfg.Firebase,
// so a PAT-only config (api_key, no refresh token) failed every tool;
// updater.go and jobs/service.go fixed the same Firebase-straggler bug.
func (e *CommandExecutor) getAuthToken(cfg *config.Config) (string, error) {
	return auth.ResolveToken(cfg)
}

func (e *CommandExecutor) buildEndpointWithCID(endpoint string, args map[string]any, cmdDef *manifest.Command, cid string) (string, error) {
	result := endpoint

	// Load config for account ID and default cluster ID
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}

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
			// Skip positional fields (already in URL path) and a cid the
			// endpoint template binds as a path segment.
			if field.Positional || skipFieldAsQueryParam(field.Name, endpoint) {
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

// skipFieldAsQueryParam reports whether a field must be left out of the
// GET / DELETE query string because the endpoint template already binds
// it as a path segment.
//
// Only `cid` needs the rule, and only when the template carries `:cid`.
// Pre-fix the skip was unconditional, which broke every ACCOUNT-scoped
// endpoint that takes cid as a FILTER rather than a path segment
// (`/:aid/vm-usage`, `/:aid/vm-events`, `/:aid/config/get`,
// `/:aid/app-info/rrc`): the filter was dropped and the read silently
// widened to the whole account. Measured on dev: vm-usage reported 152
// VMs for a cluster holding 8. Regression target: goal 19 A2 / B5.
//
// endpoint is the raw manifest template, not the substituted URL, so the
// `:cid` marker is still present when this runs.
func skipFieldAsQueryParam(fieldName, endpoint string) bool {
	return fieldName == "cid" && strings.Contains(endpoint, ":cid")
}

// coerceJSONString tries to recover from clients that ignored the manifest's
// declared field type and sent a JSON-encoded string. Only kicks in for
// `object` / `array` declared types; anything else passes through. A
// non-JSON string at an `object` field stays as-is so the downstream
// "must be an object" error still surfaces.
func coerceJSONString(val any, declaredType string) any {
	str, ok := val.(string)
	if !ok {
		return val
	}
	switch declaredType {
	case "object":
		var decoded map[string]any
		if err := json.Unmarshal([]byte(str), &decoded); err == nil {
			return decoded
		}
	case "array":
		var decoded []any
		if err := json.Unmarshal([]byte(str), &decoded); err == nil {
			return decoded
		}
	}
	return val
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
			// Defensive coercion: some MCP clients ignore the JSON Schema
			// type and send `object` / `array` fields as a JSON-encoded
			// string. Without this, `envVars: '{"K":"v"}'` reaches the
			// conductor as a literal string and the API rejects it as
			// "not an object". Decode in-place so the wire body matches
			// the manifest's declared type.
			val = coerceJSONString(val, field.Type)
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

// doRequest issues one authenticated request under ctx's deadline. ctx
// must stay live until the caller has read the response body.
func (e *CommandExecutor) doRequest(ctx context.Context, method, url string, body map[string]any, token string) (*http.Response, error) {
	return e.doRequestWithCID(ctx, method, url, body, token, "")
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

func (e *CommandExecutor) doRequestWithCID(ctx context.Context, method, url string, body map[string]any, token, cid string) (*http.Response, error) {
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

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
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

// appendMergeQuery returns endpoint with `merge=true` appended as a
// query string parameter, preserving any existing query (e.g.
// `?foo=bar` becomes `?foo=bar&merge=true`). Idempotent: a second
// call doesn't double-add. Mirrors the same-named helper in
// internal/dynacmd/executor.go (the two packages can't share a
// helper without a third-party package; the function is small enough
// that one duplicate is preferable to a `internal/util` shim that
// just exposes a 5-line string op).
func appendMergeQuery(endpoint string) string {
	if strings.Contains(endpoint, "merge=true") {
		return endpoint
	}
	if strings.Contains(endpoint, "?") {
		return endpoint + "&merge=true"
	}
	return endpoint + "?merge=true"
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
