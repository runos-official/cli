package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/runos-official/cli/internal/dynacmd"
)

// apiErrorEnvelope renders a non-2xx conductor response as the same JSON
// envelope the CLI's `--json` mode emits: `{"error": ..., "statusCode":
// ...}` plus every other top-level field conductor sent (`code`,
// `details`, `upstream`, ...).
//
// Pre-fix the MCP surface returned the bare string `API error (404):
// {"error":"...","code":"vm.not_found"}`, so an agent had to strip a
// prefix and re-parse to reach the refusal code the whole error-code
// contract exists to give it. The CLI already had the envelope; only the
// LLM-facing surface did not. Regression target: goal 21 B8.
//
// The envelope is built by dynacmd.BuildAPIErrorEnvelope so the two
// surfaces cannot drift. A body that will not marshal falls back to the
// old text shape rather than losing the response.
func apiErrorEnvelope(statusCode int, body []byte) error {
	envelope := dynacmd.BuildAPIErrorEnvelope(&dynacmd.APIError{StatusCode: statusCode, Body: body})
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("API error (%d): %s", statusCode, string(body))
	}
	return errors.New(string(encoded))
}
