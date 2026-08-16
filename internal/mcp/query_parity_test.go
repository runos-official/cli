package mcp

import (
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// topicsSearchCommand mirrors the conductor 41.0.0 manifest entry for
// mcp/topics/search. Hand-built so the test needs no network and no
// cached manifest.
func topicsSearchCommand() manifest.Command {
	return manifest.Command{
		Command:  "mcp/topics/search",
		Endpoint: "/:aid/mcp-docs/topics/search",
		Method:   "GET",
		MCP:      []string{"read"},
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "keywords", Type: "string", Required: true},
			{Name: "include", Type: "string", Default: "summary"},
		}},
	}
}

// S4, measured against conductor rc.167. `runos mcp topics search
// --keywords vms` returned one topic while the MCP tool returned zero for
// the same word. The MCP path formats a query value with Go's default
// verb, so an LLM that sends `{"keywords":["vms"]}` (an array, which the
// clients do) put the literal `[vms]` in the query string and conductor
// matched nothing. Both surfaces must produce the same query for the same
// intent.
func TestTopicsSearchQueryMatchesTheCLI(t *testing.T) {
	cmdDef := topicsSearchCommand()
	cliQuery := dynacmd.QueryParams(cmdDef, map[string]any{"keywords": "vms", "include": "summary"}).Encode()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"a plain string", map[string]any{"keywords": "vms"}},
		{"a one-element array", map[string]any{"keywords": []any{"vms"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := queryParamsFor(cmdDef, c.args, cmdDef.Endpoint).Encode()
			if got != cliQuery {
				t.Errorf("MCP query = %q, CLI query = %q", got, cliQuery)
			}
		})
	}

	t.Run("a multi-element array is comma-joined, as the field documents", func(t *testing.T) {
		got := queryParamsFor(cmdDef, map[string]any{"keywords": []any{"vms", "gpu"}}, cmdDef.Endpoint).Encode()
		want := dynacmd.QueryParams(cmdDef, map[string]any{"keywords": "vms,gpu", "include": "summary"}).Encode()
		if got != want {
			t.Errorf("MCP query = %q, want %q", got, want)
		}
	})
}

// The same formatting rule applies to a value bound into the URL path:
// mcp_topics_show with `{"key":["vm-storage"]}` must ask for the topic,
// not for `[vm-storage]`.
func TestPathValueFormatting(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"vm-storage", "vm-storage"},
		{[]any{"vm-storage"}, "vm-storage"},
		{[]any{"a", "b"}, "a,b"},
		{float64(600), "600"},
		{true, "true"},
	}
	for _, c := range cases {
		if got := dynacmd.QueryParamValue(c.in); got != c.want {
			t.Errorf("QueryParamValue(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}
