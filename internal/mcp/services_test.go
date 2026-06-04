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

// TestBuildServicesHarborBuildImageArgs_HappyPath pins the MCP->argv
// translation: always --follow + --yes (the agent blocks to terminal and a
// build failure surfaces as a non-zero exit; no stdin under MCP), cid
// stripped of any "(Name)" suffix, repeatable tags and build_arg, and the
// optional dockerfile forwarded.
func TestBuildServicesHarborBuildImageArgs_HappyPath(t *testing.T) {
	got, err := buildServicesHarborBuildImageArgs(map[string]any{
		"cid":        "mycluster (Staging)",
		"context":    "apps/backend/docker/vm-workspace",
		"repo":       "acme-vm-workspace",
		"dockerfile": "Dockerfile",
		"tags":       []any{"latest", "v1"},
		"build_arg":  []any{"GO_VERSION=1.24"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"services", "harbor", "build-image", "--follow", "--yes"} {
		if !contains(got, want) {
			t.Errorf("expected %q in argv; got %v", want, got)
		}
	}
	if !argHasValue(got, "--cid", "mycluster") {
		t.Errorf("expected --cid mycluster (suffix stripped); got %v", got)
	}
	if !argHasValue(got, "--context", "apps/backend/docker/vm-workspace") {
		t.Errorf("expected --context; got %v", got)
	}
	if !argHasValue(got, "--repo", "acme-vm-workspace") {
		t.Errorf("expected --repo; got %v", got)
	}
	if !argHasValue(got, "--dockerfile", "Dockerfile") {
		t.Errorf("expected --dockerfile; got %v", got)
	}

	var tagCount int
	for i, s := range got {
		if s == "--tag" && i+1 < len(got) {
			tagCount++
		}
	}
	if tagCount != 2 {
		t.Errorf("--tag occurrences = %d, want 2; got %v", tagCount, got)
	}
	if !argHasValue(got, "--build-arg", "GO_VERSION=1.24") {
		t.Errorf("expected --build-arg GO_VERSION=1.24; got %v", got)
	}
}

// TestBuildServicesHarborBuildImageArgs_Required pins the required-arg
// errors: context, repo, and at least one non-blank tag.
func TestBuildServicesHarborBuildImageArgs_Required(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing context", map[string]any{"repo": "r", "tags": []any{"latest"}}},
		{"missing repo", map[string]any{"context": ".", "tags": []any{"latest"}}},
		{"missing tags", map[string]any{"context": ".", "repo": "r"}},
		{"empty tags", map[string]any{"context": ".", "repo": "r", "tags": []any{}}},
		{"tags all blank", map[string]any{"context": ".", "repo": "r", "tags": []any{"  ", ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildServicesHarborBuildImageArgs(tc.args); err == nil {
				t.Errorf("expected error for %s; got nil", tc.name)
			}
		})
	}
}

// argHasValue reports whether argv contains flag immediately followed by value.
func argHasValue(argv []string, flag, value string) bool {
	for i, s := range argv {
		if s == flag && i+1 < len(argv) && argv[i+1] == value {
			return true
		}
	}
	return false
}
