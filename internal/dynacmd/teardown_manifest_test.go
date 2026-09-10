package dynacmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

func TestNodeTeardownManifestCommandsRegister(t *testing.T) {
	testManifest := &manifest.Manifest{Commands: []manifest.Command{
		{
			Command:  "node-teardowns/list",
			Endpoint: "/:aid/:cid/node-teardowns",
			Method:   "GET",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "nid", Type: "string"},
				{Name: "jobId", Type: "string"},
				{Name: "limit", Type: "integer"},
				{Name: "cursor", Type: "string"},
			}},
			Output: &manifest.Output{Type: "array"},
		},
		{
			Command:  "node-teardowns/show",
			Endpoint: "/:aid/:cid/node-teardowns/{id}",
			Method:   "GET",
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Type: "string", Required: true, Positional: true},
			}},
			Output: &manifest.Output{Type: "object"},
		},
	}}

	roots := NewBuilder(testManifest, NewExecutor("https://example.test")).BuildCommands()
	var root *cobra.Command
	for _, command := range roots {
		if command.Name() == "node-teardowns" {
			root = command
			break
		}
	}
	if root == nil {
		t.Fatal("node-teardowns command was not registered")
	}

	list, _, err := root.Find([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"nid", "job-id", "limit", "cursor", "cid"} {
		if list.Flags().Lookup(flag) == nil {
			t.Errorf("list command missing --%s", flag)
		}
	}

	show, _, err := root.Find([]string{"show"})
	if err != nil {
		t.Fatal(err)
	}
	if err := show.Args(show, []string{"11111111-1111-4111-8111-111111111111"}); err != nil {
		t.Fatalf("show rejected required positional id: %v", err)
	}
	if !strings.Contains(show.Use, "<id>") {
		t.Fatalf("show usage does not identify the required positional id: %s", show.Use)
	}
}
