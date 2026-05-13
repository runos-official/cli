package mcp

import (
	"strings"
	"testing"
)

// I27-L regression: buildServicesPullArgs must translate the MCP schema's
// `service_id` field to the CLI flag `--id` (not `--service-id`). The
// CLI's `runos services pull` registers `--id` only; passing
// `--service-id` would surface as `unknown flag: --service-id` to the
// MCP caller.
func TestBuildServicesPullArgs_ServiceIDMapsToIDFlag(t *testing.T) {
	got := buildServicesPullArgs(map[string]any{
		"type":       "postgresql",
		"service_id": "mysvc",
		"cid":        "mycluster2",
		"out":        ".",
	})
	for _, want := range []string{"--type", "postgresql", "--id", "mysvc", "--cid", "mycluster2", "--out", "."} {
		if !contains(got, want) {
			t.Errorf("argv missing %q; got %v", want, got)
		}
	}
	for _, refused := range []string{"--service-id", "--service_id"} {
		for _, a := range got {
			if a == refused {
				t.Errorf("argv must not contain %q (CLI flag is --id); got %v", refused, got)
			}
		}
	}
}

func TestBuildServicesPullArgs_PassesForceAndYAMLPositional(t *testing.T) {
	got := buildServicesPullArgs(map[string]any{
		"yaml_file": "/abs/path/runos.service.mycluster2.mysvc.yaml",
		"force":     true,
	})
	if !contains(got, "--force") {
		t.Errorf("argv missing --force; got %v", got)
	}
	if !contains(got, "/abs/path/runos.service.mycluster2.mysvc.yaml") {
		t.Errorf("argv missing yaml positional; got %v", got)
	}
	// `--` must precede the yaml positional so a yaml path starting with `-`
	// can't be reinterpreted as a flag.
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-- /abs/path/runos.service.mycluster2.mysvc.yaml") {
		t.Errorf("expected `-- <yaml>` at tail; got %q", joined)
	}
}
