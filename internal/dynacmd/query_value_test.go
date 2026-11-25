package dynacmd

import (
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Review findings 1194/1196: a field the manifest marks Location:"header"
// must never land in the GET/DELETE query string, on either the cobra
// path (buildEndpoint) or the MCP path (internal/mcp/executor.go calls
// this same function). A field with no Location (the ordinary case)
// must keep working exactly as before.
func TestQueryParams_SkipsHeaderLocatedFields(t *testing.T) {
	cmdDef := manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "credential", Positional: true},
		{Name: "ifMatch", Location: "header"},
		{Name: "detail"},
	}}}
	values := map[string]any{"credential": "c1", "ifMatch": `"etag-0"`, "detail": "true"}

	got := QueryParams(cmdDef, values)
	if got.Has("ifMatch") {
		t.Errorf("a header-located field must never reach the query string, got: %v", got)
	}
	if got.Has("credential") {
		t.Errorf("a positional field must never reach the query string, got: %v", got)
	}
	if got.Get("detail") != "true" {
		t.Errorf("an ordinary field must still reach the query string, got: %v", got)
	}
}
