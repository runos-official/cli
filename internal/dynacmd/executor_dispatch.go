package dynacmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/runos-official/cli/internal/apitimeout"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// appendMergeQuery returns endpoint with `merge=true` appended as a
// query string parameter, preserving any existing query (e.g.
// `?foo=bar` becomes `?foo=bar&merge=true`). Idempotent: a second
// call doesn't double-add. Pure string operation; no URL parsing.
func appendMergeQuery(endpoint string) string {
	if strings.Contains(endpoint, "merge=true") {
		return endpoint
	}
	if strings.Contains(endpoint, "?") {
		return endpoint + "&merge=true"
	}
	return endpoint + "?merge=true"
}

// dispatch is the shared HTTP path used by both Execute and
// ExecuteWithInput. It builds the endpoint, filters out path-param fields
// from the body, sends the request, and reads the response. Non-2xx
// responses are returned as *APIError so callers can branch on status.
func (e *Executor) dispatch(cmdDef manifest.Command, args []string, body map[string]any, cid string, cfg *config.Config, token string) ([]byte, error) {
	return e.dispatchWithOptions(cmdDef, args, body, cid, cfg, token, requestOptions{})
}

func (e *Executor) dispatchWithOptions(cmdDef manifest.Command, args []string, body map[string]any, cid string, cfg *config.Config, token string, options requestOptions) ([]byte, error) {
	endpoint, err := e.buildEndpoint(cmdDef.Endpoint, args, cmdDef, cfg, cid, body)
	if err != nil {
		return nil, err
	}
	// I4-K CLI follow-up: `apps update` is a partial-PATCH command (the
	// user supplies a few fields, e.g. `--replicas 3`). Without
	// `?merge=true` the conductor's pre-fix desired-state semantics
	// silently zero cpu/memory and clear the 5 healthCheck/metrics
	// fields whenever they're omitted. The conductor shipped the merge
	// param specifically for partial-PATCH callers; the dynacmd
	// dispatch path opts in here so every CLI surface (and the MCP
	// wrapper that shells via dynacmd) gets the safe semantics.
	if cmdDef.Command == "apps/update" {
		endpoint = appendMergeQuery(endpoint)
	}
	requestBody := filterPathParamsFromBody(body, cmdDef)
	// The deadline covers the response read as well, so the cancel stays
	// live until this function has drained the body (A4).
	ctx, cancel := context.WithTimeout(context.Background(), apitimeout.For(cmdDef, body))
	defer cancel()
	resp, err := e.doRequest(ctx, cmdDef.Method, endpoint, requestBody, token, options)
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
	// Defensive: some conductor handlers wrap their own error in the 200
	// response body instead of letting the framework's error middleware
	// emit a real 4xx status (observed on `apps builds` returning
	// `{"error":"App 'X' not found","statusCode":404}` with HTTP 200,
	// I11-Q). The CLI used to dump the envelope through the JSON
	// formatter and exit 0, silently passing a "not found" as success in
	// CI pipelines. Synthesise an *APIError from the embedded statusCode
	// so the standard error path runs and the exit code is non-zero.
	if apiErr := apiErrorFromBody(resp.StatusCode, respBody); apiErr != nil {
		return nil, apiErr
	}
	return respBody, nil
}

// apiErrorFromBody recognises the conductor's error-envelope shape
// (`{"error": string, "statusCode": int >= 400}`) inside an otherwise
// successful 2xx response. Returns nil for any other shape (so happy-path
// 2xx bodies pass through unchanged) and for any envelope whose embedded
// statusCode is not a client/server error code. The check requires both
// keys to avoid false positives on legitimate payloads that happen to
// include just one of them.
func apiErrorFromBody(httpStatus int, body []byte) *APIError {
	if httpStatus >= 400 {
		return nil
	}
	var envelope struct {
		Error      string `json:"error"`
		StatusCode int    `json:"statusCode"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	if envelope.Error == "" || envelope.StatusCode < 400 {
		return nil
	}
	return &APIError{StatusCode: envelope.StatusCode, Body: body}
}
