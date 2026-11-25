package mcp

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// requestFailed turns a transport failure into the error an MCP call returns. The version gate's
// refusal becomes the same JSON envelope as any other conductor refusal (code, statusCode, one plain
// sentence), so an agent reads the code instead of a request URL.
func requestFailed(err error) error {
	var gate *api.UpdateRequiredError
	if errors.As(err, &gate) {
		if encoded, mErr := json.MarshalIndent(gate.Envelope(), "", "  "); mErr == nil {
			return errors.New(string(encoded))
		}
		return gate
	}
	return fmt.Errorf("request failed: %w", err)
}

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
//
// Story 347 M5 (finding 1196): before the body is embedded, apply the
// same credential error-body guard the cobra path runs (story 343,
// dynacmd.GuardCredentialErrorBody) for cmdDef's family. A secret-shaped
// value anywhere in a credential-family error body is replaced by a
// generic refusal that carries none of the original text, rather than
// letting the embedded error envelope repeat it back to the caller.
func apiErrorEnvelope(cmdDef manifest.Command, statusCode int, body []byte) error {
	apiErr := &dynacmd.APIError{StatusCode: statusCode, Body: body}
	if guarded := dynacmd.GuardCredentialErrorBody(cmdDef, apiErr); guarded != error(apiErr) {
		return guarded
	}
	envelope := dynacmd.BuildAPIErrorEnvelope(apiErr)
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("API error (%d): %s", statusCode, string(body))
	}
	return errors.New(string(encoded))
}
