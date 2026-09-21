package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/runos-official/cli/internal/services"
)

func TestServicesSyncTextPreviewHidesBareAffinity(t *testing.T) {
	update := &manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "replicas", Type: "integer"},
		{Name: "nodeAffinityTags", Type: "array", Clearable: true},
	}}}
	server := &services.ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"replicas": 1, "nodeAffinityTags": []any{"gpu:shared"}}}
	for _, tc := range []struct {
		name, edit string
		fields     map[string]any
		wantPin    bool
	}{
		{"replica only", "replicas", map[string]any{"replicas": 2, "nodeAffinityTags": nil}, false},
		{"deleted pin", "removed", map[string]any{"replicas": 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := &services.ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: tc.fields}
			plan := services.ComputeSyncPlan(local, server, nil, update, nil)
			out := captureStdout(t, func() { printServicesSyncPlan(plan, false) })
			if !strings.Contains(out, tc.edit) {
				t.Fatalf("preview lacks %s: %s", tc.edit, out)
			}
			if got := strings.Contains(out, "nodeAffinityTags"); got != tc.wantPin {
				t.Fatalf("affinity visible=%t, want %t: %s", got, tc.wantPin, out)
			}
		})
	}
}
