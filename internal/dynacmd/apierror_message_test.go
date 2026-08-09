package dynacmd

import (
	"strings"
	"testing"
)

func TestDescribeAPIError(t *testing.T) {
	// Conductor writes a message for a person and the CLI was printing the envelope it arrived
	// in, so a 404 reached the terminal as:
	//
	//   Error: API error (404): {"error":"VM nope1 not found on this cluster.","code":"vm.not_found","traceId":"a136..."}
	//
	// Measured against dev 2026-08-09. The message is right there and every other field is
	// noise to a reader, or a second call to a machine that now has to parse an error string.

	t.Run("says what conductor said", func(t *testing.T) {
		got := describeAPIError(404, []byte(`{"error":"VM nope1 not found on this cluster."}`))
		if !strings.HasPrefix(got, "VM nope1 not found on this cluster.") {
			t.Fatalf("got %q, want it to lead with the message", got)
		}
		if strings.Contains(got, "{") {
			t.Fatalf("got %q, want no JSON", got)
		}
	})

	t.Run("keeps the status, which is what separates 404 from 403 on a vague message", func(t *testing.T) {
		if got := describeAPIError(403, []byte(`{"error":"Forbidden"}`)); !strings.Contains(got, "403") {
			t.Fatalf("got %q, want the status", got)
		}
	})

	t.Run("keeps the machine-readable code when there is one", func(t *testing.T) {
		// A code is the thing a caller is supposed to branch on, so dropping it would push
		// callers back to matching the message, which is written to be reworded.
		got := describeAPIError(404, []byte(`{"error":"VM x not found.","code":"vm.not_found"}`))
		if !strings.Contains(got, "vm.not_found") {
			t.Fatalf("got %q, want the code", got)
		}
	})

	t.Run("omits the code when there is none, rather than printing an empty one", func(t *testing.T) {
		if got := describeAPIError(404, []byte(`{"error":"Gone"}`)); strings.Contains(got, ", )") {
			t.Fatalf("got %q, want no empty code", got)
		}
	})

	t.Run("never hides a body it cannot read", func(t *testing.T) {
		// A proxy's HTML error page, or an empty body. Swallowing it would turn the only
		// evidence into "request failed".
		got := describeAPIError(502, []byte("<html>bad gateway</html>"))
		if !strings.Contains(got, "bad gateway") || !strings.Contains(got, "502") {
			t.Fatalf("got %q, want the raw body and the status", got)
		}
	})

	t.Run("says something usable for an empty body", func(t *testing.T) {
		got := describeAPIError(500, nil)
		if got == "" || !strings.Contains(got, "500") {
			t.Fatalf("got %q, want the status at least", got)
		}
	})

	t.Run("falls back to the raw body when the JSON carries no message", func(t *testing.T) {
		got := describeAPIError(400, []byte(`{"detail":"something else"}`))
		if !strings.Contains(got, "something else") {
			t.Fatalf("got %q, want the body preserved", got)
		}
	})

	t.Run("does not leak the trace id into every error line", func(t *testing.T) {
		// It is in the body for a support conversation and available with --json; putting it
		// on every failure buries the sentence that matters.
		got := describeAPIError(404, []byte(`{"error":"Gone","traceId":"a136-c067"}`))
		if strings.Contains(got, "a136-c067") {
			t.Fatalf("got %q, want no trace id", got)
		}
	})
}
