package services

import (
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
