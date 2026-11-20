package dynacmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/runos-official/cli/internal/manifest"
)

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

type requestOptions struct {
	includeEmptyJSONObject bool
}

// headerFieldNames returns the names of cmdDef's input fields declared
// with Location "header", as a set.
func headerFieldNames(cmdDef manifest.Command) map[string]bool {
	if cmdDef.Input == nil {
		return nil
	}
	var names map[string]bool
	for _, f := range cmdDef.Input.Fields {
		if f.Location == "header" {
			if names == nil {
				names = make(map[string]bool)
			}
			names[f.Name] = true
		}
	}
	return names
}

// SplitHeaderFields moves every field cmdDef declares with Location
// "header" (review finding 1151: Conductor's typed credential-lifecycle
// commands mark idempotencyKey/ifMatch this way) out of body and into a
// real http.Header, keyed by the field's canonical HTTP header name
// (headerNameFromFieldName). body is returned with those keys removed;
// callers that go on to unflatten/JSON-encode body never see them.
// Returns (body, nil) unchanged when cmdDef declares no header field, so
// every existing command is unaffected.
func SplitHeaderFields(body map[string]any, cmdDef manifest.Command) (map[string]any, http.Header) {
	names := headerFieldNames(cmdDef)
	if len(names) == 0 {
		return body, nil
	}
	remaining := make(map[string]any, len(body))
	headers := http.Header{}
	for k, v := range body {
		if names[k] {
			if s, ok := v.(string); ok && s != "" {
				headers.Set(headerNameFromFieldName(k), s)
			}
			continue
		}
		remaining[k] = v
	}
	return remaining, headers
}

// headerNameFromFieldName converts a manifest camelCase field name
// (idempotencyKey, ifMatch) into its canonical HTTP header name
// (Idempotency-Key, If-Match): split at each lowercase-to-uppercase
// transition, capitalize each resulting word, join with hyphens. This
// is a general, testable transform, not a lookup table of the two field
// names known today, so a future header-located field needs no code
// change here -- only a manifest entry with Location: "header".
func headerNameFromFieldName(name string) string {
	var words []string
	var cur []rune
	for _, r := range name {
		if unicode.IsUpper(r) && len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		words = append(words, string(cur))
	}
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, "-")
}

// doRequest issues one authenticated request under ctx's deadline. ctx
// must stay live until the caller has read the response body. headers
// carries any Location:"header" field values (SplitHeaderFields); nil
// or empty is the common case and adds nothing beyond Authorization/
// Content-Type.
func (e *Executor) doRequest(ctx context.Context, method, url string, body map[string]any, headers http.Header, token string, options requestOptions) (*http.Response, error) {
	var bodyReader io.Reader

	writeJSON := (method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch) &&
		(len(body) > 0 || (method == http.MethodPost && options.includeEmptyJSONObject))
	if writeJSON {
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
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}

	return e.httpClient.Do(req)
}
