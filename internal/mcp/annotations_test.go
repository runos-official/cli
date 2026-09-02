package mcp

import (
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// A client cannot tell a read from a write without these hints. Cursor's
// "Writes: Don't allow" policy disabled all 315 tools on the READ server
// because every one arrived unannotated and had to be assumed a write.
func TestAnnotationsForDerivesReadOnlyFromMethod(t *testing.T) {
	got := annotationsFor(manifest.Command{Command: "services/list", Method: "GET"})
	if got == nil || !got.ReadOnlyHint {
		t.Fatalf("a GET must be readOnlyHint true, got %+v", got)
	}
	if got.DestructiveHint {
		t.Fatalf("a GET must never be destructive, got %+v", got)
	}
}

func TestAnnotationsForMarksDeleteDestructive(t *testing.T) {
	got := annotationsFor(manifest.Command{Command: "apps/delete", Method: "DELETE"})
	if got == nil || got.ReadOnlyHint {
		t.Fatalf("a DELETE must not be read-only, got %+v", got)
	}
	if !got.DestructiveHint {
		t.Fatalf("a DELETE must be destructiveHint true, got %+v", got)
	}
}

func TestAnnotationsForPlainWriteIsNeitherReadOnlyNorDestructive(t *testing.T) {
	got := annotationsFor(manifest.Command{Command: "apps/restart", Method: "POST"})
	if got == nil {
		t.Fatal("annotations must never be nil: absent means unknown to a client")
	}
	if got.ReadOnlyHint {
		t.Fatalf("a POST must not be read-only, got %+v", got)
	}
}

// The serving tier, not the HTTP method, is the authority. Several genuine
// reads are POSTs because they need a request body; keying on the method alone
// marked seven of them as writes.
func TestAnnotationsForReadTierBeatsPostMethod(t *testing.T) {
	got := annotationsFor(manifest.Command{
		Command: "virt/config-diff", Method: "POST", MCP: []string{"read"},
	})
	if got == nil || !got.ReadOnlyHint {
		t.Fatalf("a POST on the read tier must be readOnlyHint true, got %+v", got)
	}
}

func TestAnnotationsForWriteTierIsNeverReadOnly(t *testing.T) {
	got := annotationsFor(manifest.Command{
		Command: "apps/restart", Method: "POST", MCP: []string{"write"},
	})
	if got == nil || got.ReadOnlyHint {
		t.Fatalf("a write tier must never be read-only, got %+v", got)
	}
}

func TestAnnotationsForSensitiveReadIsReadOnly(t *testing.T) {
	got := annotationsFor(manifest.Command{
		Command: "services/valkey/credentials", Method: "GET", MCP: []string{"sensitive_read"},
	})
	if got == nil || !got.ReadOnlyHint {
		t.Fatalf("sensitive_read must be readOnlyHint true, got %+v", got)
	}
}
