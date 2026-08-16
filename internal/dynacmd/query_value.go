package dynacmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// QueryParams assembles the query string for GET/DELETE requests from a
// manifest command's input definition plus the resolved value map.
// Includes every non-positional Field plus every Flag whose value is
// present; positional Fields are skipped (they substitute into the URL
// path elsewhere).
//
// Regression target: I7-F. Pre-fix the GET branch in buildEndpoint only
// iterated Fields, silently dropping Flags. `apps_logs --previous`
// reached the conductor without the previous=true query param, so the
// server-side `if (previous)` gate never fired.
//
// Exported because internal/mcp builds the same query for the same
// commands. Two builders disagreed once already (S4: the MCP surface
// returned 0 topics for a keyword the CLI found), and one function is the
// only way they cannot disagree again.
func QueryParams(cmdDef manifest.Command, values map[string]any) url.Values {
	queryParams := url.Values{}
	if cmdDef.Input == nil {
		return queryParams
	}
	for _, field := range cmdDef.Input.Fields {
		if field.Positional {
			continue
		}
		if val, ok := values[field.Name]; ok {
			queryParams.Set(field.Name, QueryParamValue(val))
		}
	}
	for _, flag := range cmdDef.Input.Flags {
		if val, ok := values[flag.Name]; ok {
			queryParams.Set(flag.Name, QueryParamValue(val))
		}
	}
	return queryParams
}

// QueryParamValue renders one argument as the string that goes into a
// query string or a URL path segment.
//
// Go's `%v` renders a slice as `[a b]`, and MCP clients send a string
// field as a one-element array often enough that this was a live defect:
// `mcp_topics_search {"keywords":["vms"]}` asked conductor for the
// keyword `[vms]` and got nothing back, while `runos mcp topics search
// --keywords vms` found the topic (S4). A list is comma-joined, which is
// the shape every list-valued RunOS query parameter documents. An object
// is JSON, because there is no other honest rendering of one.
func QueryParamValue(val any) string {
	switch v := val.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		return joinValues(v)
	case []string:
		parts := make([]any, len(v))
		for i, s := range v {
			parts[i] = s
		}
		return joinValues(parts)
	case map[string]any:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(encoded)
	}
	return fmt.Sprintf("%v", val)
}

// joinValues renders a list argument as the comma-separated form.
func joinValues(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, QueryParamValue(item))
	}
	return strings.Join(parts, ",")
}
