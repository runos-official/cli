package mcp

import (
	"slices"
	"strings"
	"testing"
)

// buildRunArgs is the only piece of the run MCP tool that's pure: it
// translates the MCP arg map into a runos argv. The cobra-level
// subprocess path is exercised end-to-end by the implementer agent's
// final verification and not covered here, per the project's testing
// convention (no cobra subprocess tests).
func TestBuildRunArgs(t *testing.T) {
	t.Run("happy path with all flags", func(t *testing.T) {
		got, err := buildRunArgs(map[string]any{
			"app":     "appid7",
			"sha":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"cid":     "mycluster",
			"timeout": "30m",
			"command": []any{"scripts/release.sh"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			"run",
			"--app", "appid7",
			"--sha", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"--cid", "mycluster",
			"--timeout", "30m",
			"-y",
			"--", "scripts/release.sh",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("argv mismatch\n got: %#v\nwant: %#v", got, want)
		}
	})

	t.Run("native []string command also accepted", func(t *testing.T) {
		got, err := buildRunArgs(map[string]any{
			"app":     "appid7",
			"sha":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"command": []string{"alembic", "upgrade", "head"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// trailing slice carries the multi-arg command verbatim
		if !slices.Equal(got[len(got)-4:], []string{"--", "alembic", "upgrade", "head"}) {
			t.Fatalf("trailing argv mismatch\n got: %#v", got)
		}
	})

	t.Run("optional flags are omitted when absent", func(t *testing.T) {
		got, err := buildRunArgs(map[string]any{
			"app":     "appid7",
			"sha":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"command": []any{"go", "doit"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// must NOT carry --cid or --timeout
		joined := strings.Join(got, " ")
		if strings.Contains(joined, "--cid") || strings.Contains(joined, "--timeout") {
			t.Fatalf("expected no --cid/--timeout in argv; got: %#v", got)
		}
		// must still carry -y so the subprocess doesn't try to read TTY
		if !slices.Contains(got, "-y") {
			t.Fatalf("expected -y in argv; got: %#v", got)
		}
	})

	t.Run("command is required", func(t *testing.T) {
		_, err := buildRunArgs(map[string]any{
			"app": "appid7",
			"sha": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		})
		if err == nil {
			t.Fatalf("expected error when command absent")
		}
	})

	t.Run("empty command entry is refused", func(t *testing.T) {
		_, err := buildRunArgs(map[string]any{
			"app":     "appid7",
			"sha":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"command": []any{"scripts/migrate.sh", ""},
		})
		if err == nil {
			t.Fatalf("expected error on empty argv entry")
		}
	})

	t.Run("non-string command entry is refused", func(t *testing.T) {
		_, err := buildRunArgs(map[string]any{
			"app":     "appid7",
			"sha":     "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"command": []any{"go", 7},
		})
		if err == nil {
			t.Fatalf("expected error on non-string argv entry")
		}
	})
}

// isStaticRunTool predicate is the dispatch key in server.go; pin its
// behaviour so a typo doesn't silently route through the manifest
// executor instead of handleRun.
func TestIsStaticRunTool(t *testing.T) {
	if !isStaticRunTool("run") {
		t.Fatal("isStaticRunTool(\"run\") = false; want true")
	}
	if isStaticRunTool("runs") || isStaticRunTool("RUN") || isStaticRunTool("apps_run") {
		t.Fatal("isStaticRunTool matched a non-run name")
	}
}

// staticRunTools is gated on the sensitive_write MCP category, mirroring
// the existing pattern for deploy. Pin so the verb doesn't leak into
// the read-only surface (where a tool that mutates cluster state has
// no business being).
func TestStaticRunToolsCategoryGating(t *testing.T) {
	if got := staticRunTools("read"); len(got) != 0 {
		t.Fatalf("staticRunTools(read) = %d tools; want 0", len(got))
	}
	if got := staticRunTools("sensitive_write"); len(got) != 1 || got[0].Name != "run" {
		t.Fatalf("staticRunTools(sensitive_write) didn't yield exactly the run tool; got %+v", got)
	}
}
