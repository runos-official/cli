package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// advisoryWarningsKey is the response key conductor uses for an advisory
// that has to reach the caller at the moment of the call rather than in a
// job log that arrives later. Objective 92 plan header, settled item 4
// fixes the shape: an array of plain strings, top level, beside the
// ordinary body.
//
// Only the PLURAL key is ours. The singular `warning` string belongs to
// the one-shot-token banners (printApiKeyTokenBanner,
// printNotifyKeyBanner) and to a handful of per-command fields that the
// table renders in place; touching it here would print those twice.
const advisoryWarningsKey = "warnings"

// AdvisoryWarnings returns the advisory strings a response body carries
// under the top-level `warnings` key, in body order, and reports whether
// EVERY entry of that array was a string.
//
// The second return value is what makes the caller's suppression safe:
// the table row may only be stripped when nothing in the array went
// unprinted. A future richer entry (an object, say) is skipped rather
// than rendered, because `map[...]` at an operator is worse than no
// line at all, and the false `complete` then keeps the key in the body
// so the existing forward-compat row still shows it.
//
// Total on malformed input: a missing key, a body that is not a JSON
// object, a `warnings` value that is not an array, a null, an empty
// array and unparseable bytes all return (nil, false).
func AdvisoryWarnings(respBody []byte) (lines []string, complete bool) {
	trimmed := bytes.TrimSpace(respBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, false
	}
	raw, ok := probe[advisoryWarningsKey]
	if !ok {
		return nil, false
	}
	var entries []any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	complete = true
	for _, entry := range entries {
		s, ok := entry.(string)
		if !ok {
			complete = false
			continue
		}
		lines = append(lines, s)
	}
	if len(lines) == 0 {
		return nil, false
	}
	return lines, complete
}

// stripAdvisoryWarnings returns respBody without its top-level
// `warnings` key, so the plain-text table does not repeat text the
// caller has already printed as warning lines. Returns respBody
// unchanged when the body is not a JSON object or carries no such key.
//
// Re-serialising is safe for the table: formatObject drives column
// order from the manifest's declared field list and sorts the
// undeclared remainder, so it never depends on the byte order of the
// response. The JSON output mode never calls this, which is what keeps
// `--json` structurally identical to what conductor sent.
func stripAdvisoryWarnings(respBody []byte) []byte {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(respBody), &probe); err != nil {
		return respBody
	}
	if _, ok := probe[advisoryWarningsKey]; !ok {
		return respBody
	}
	delete(probe, advisoryWarningsKey)
	stripped, err := json.Marshal(probe)
	if err != nil {
		return respBody
	}
	return stripped
}

// PrintAdvisoryWarnings writes one `Warning: <entry>` line per advisory.
// The text and the stream are copied from `runos deploy`, the only
// reader of a conductor advisory before this (cmd/deploy.go), so an
// operator sees the same shape whichever command they ran.
//
// Exported, with AdvisoryWarnings, because `Execute` is NOT the only
// entry point that reaches a conductor advisory: `runos services sync`
// applies its plan through ExecuteWithInput (internal/services/sync.go),
// which does no rendering of its own. Both surfaces call these two so
// there is one text and one stream, never a second renderer.
func PrintAdvisoryWarnings(w io.Writer, lines []string) {
	for _, line := range lines {
		fmt.Fprintf(w, "Warning: %s\n", line)
	}
}
