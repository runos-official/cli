package services

import (
	"reflect"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func TestServiceComparisonPreviewShowsOnlySemanticDrift(t *testing.T) {
	update := &manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "replicas", Type: "integer"},
		{Name: "gpuCount", Type: "number"},
	}}}
	local := &ServiceYAML{ID: "abc12", Fields: map[string]any{"replicas": 2, "gpuCount": 1}}
	server := &ServiceYAML{ID: "abc12", Fields: map[string]any{"replicas": 1, "gpuCount": float64(1)}}
	comparison := CompareServiceState(local, server, update, nil)
	if !comparison.HasDrift || !strings.Contains(comparison.Diff, "replicas") {
		t.Fatalf("replica drift missing: %#v", comparison)
	}
	if strings.Contains(comparison.Diff, "gpuCount") {
		t.Fatalf("equivalent numeric value appears as drift: %s", comparison.Diff)
	}
}

func TestServiceComparisonPreviewNormalizesLargeEqualNumbers(t *testing.T) {
	update, err := UpdateCommand(productionClearableManifest(t), "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	local := loadComparisonYAML(t, "postgresql", "replicas: 2\nstorageMb: 1000000\nnodeAffinityTags:\n")
	server := &ServiceYAML{Type: "postgresql", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{
		"replicas": float64(1), "storageMb": float64(1000000), "nodeAffinityTags": []any{"gpu:shared"},
	}}
	comparison := CompareServiceState(local, server, update, nil)
	if !reflect.DeepEqual(comparison.PatchBody, map[string]any{"replicas": 2}) {
		t.Fatalf("patch = %#v", comparison.PatchBody)
	}
	diff := ComputeSemanticDiff("service.yaml", local, server, update, nil)
	if diff.Status != StatusDrift || !strings.Contains(diff.UnifiedDiff, "replicas") {
		t.Fatalf("replica drift missing: %#v", diff)
	}
	if strings.Contains(diff.UnifiedDiff, "storageMb") || strings.Contains(diff.UnifiedDiff, "nodeAffinityTags") {
		t.Fatalf("equal storage or bare affinity appears as drift: %s", diff.UnifiedDiff)
	}
	for _, fields := range []string{
		"replicas: 2\nstorageMb: '1000000'\nnodeAffinityTags:\n",
		"replicas: 2\nstorageMb: 1000001\nnodeAffinityTags:\n",
		"replicas: 2\nstorageMb: 1000000\nnodeAffinityTags:\nunknownField: changed\n",
	} {
		local = loadComparisonYAML(t, "postgresql", fields)
		diff = ComputeSemanticDiff("service.yaml", local, server, update, nil)
		visible := "storageMb"
		if strings.Contains(fields, "unknownField") {
			visible = "unknownField"
		}
		if diff.Status != StatusDrift || !strings.Contains(diff.UnifiedDiff, visible) {
			t.Fatalf("real edit %q hidden: %#v", visible, diff)
		}
	}
}
