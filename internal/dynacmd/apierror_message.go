package dynacmd

import (
	"encoding/json"
	"fmt"
	"strings"
)

// describeAPIError renders a failed Conductor call as one line a person can act on.
//
// Conductor writes its error messages for a reader and returns them in an envelope alongside a
// machine-readable code and a trace id. Printing the envelope put the sentence that matters
// inside a JSON blob, so what reached the terminal was:
//
//	API error (404): {"error":"VM nope1 not found on this cluster.","code":"vm.not_found","traceId":"a136..."}
//
// Three things survive, and nothing else. The MESSAGE, because it is the answer. The STATUS,
// because a vague message reads very differently at 403 than at 404. And the CODE when there is
// one, because that is what a caller is meant to branch on: a message is expected to be
// reworded, so anything keying off it breaks silently the first time it improves.
//
// A body that is not the expected JSON is passed through untouched. A proxy's HTML error page
// is evidence, and swallowing it would turn the only clue into "request failed".
func describeAPIError(statusCode int, body []byte) string {
	raw := strings.TrimSpace(string(body))

	var envelope struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error != "" {
		if envelope.Code != "" {
			return fmt.Sprintf("%s (HTTP %d, %s)", envelope.Error, statusCode, envelope.Code)
		}
		return fmt.Sprintf("%s (HTTP %d)", envelope.Error, statusCode)
	}

	if raw == "" {
		return fmt.Sprintf("the request failed (HTTP %d)", statusCode)
	}
	return fmt.Sprintf("the request failed (HTTP %d): %s", statusCode, raw)
}
