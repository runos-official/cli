package roidapi

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// testdata/rbac.yaml is a copy of roid's OpenAPI document for the access
// API (docs/api/rbac.yaml in the roid repo). Refresh it with
// scripts/sync-roid-openapi.sh when roid's document changes; this test then
// names every operation the client is missing or carries in excess.
func TestOperationsMatchTheOpenAPIDocument(t *testing.T) {
	raw, err := os.ReadFile("testdata/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]operation{}
	for path, methods := range doc.Paths {
		for method, o := range methods {
			want[o.OperationID] = operation{method: strings.ToUpper(method), path: path}
		}
	}
	var missing, extra, wrong []string
	for id, w := range want {
		got, ok := operations[id]
		switch {
		case !ok:
			missing = append(missing, id)
		case got.method != w.method || got.path != w.path:
			wrong = append(wrong, id+": "+got.method+" "+got.path+" != "+w.method+" "+w.path)
		}
	}
	for id := range operations {
		if _, ok := want[id]; !ok {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(wrong)
	if len(missing)+len(extra)+len(wrong) > 0 {
		t.Fatalf("operations drift from the OpenAPI document:\nmissing %v\nextra %v\nwrong %v", missing, extra, wrong)
	}
}
