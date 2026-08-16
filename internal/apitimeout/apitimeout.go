// Package apitimeout derives the per-call HTTP deadline for one dispatch
// of a manifest command, so a synchronous endpoint that legitimately runs
// for minutes is not cut off by a fixed client-wide timeout.
//
// Both CLI surfaces (internal/dynacmd for cobra, internal/mcp for MCP
// tools) used a single 30 s http.Client timeout. Several conductor
// endpoints are synchronous and slower than that by design:
// `vms/run-command` accepts a `timeoutSeconds` up to 600, and
// `vms/nvlink-check`, `vms/rotate-ssh-key`, `vms/screenshot`,
// `virt/reapply` and `storage-groups/wipe-device` all wait on cluster
// work before answering. The 30 s cut arrived as `context deadline
// exceeded` while the server kept going, which reads as a failure for an
// operation that then succeeds invisibly. Regression target: goal 19 A4.
package apitimeout

import (
	"time"

	"github.com/runos-official/cli/internal/manifest"
)

// Default is the deadline for an ordinary call. Unchanged from the
// previous client-wide timeout, so nothing fast gets slower to fail.
const Default = 30 * time.Second

// LongRunning is the deadline for a synchronous endpoint that waits on
// cluster work but declares no timeout of its own. 11 minutes: one
// minute past the 600 s ceiling conductor accepts anywhere, so the
// server's own deadline always fires first and the caller gets the
// server's reason instead of a bare client timeout.
const LongRunning = 660 * time.Second

// headroom is added to a caller-declared server-side budget so the
// server's own timeout fires first and its error message (which says
// what was killed and why) reaches the caller.
const headroom = 60 * time.Second

// timeoutFieldName is the manifest field through which a caller states
// the server-side budget for a synchronous command.
const timeoutFieldName = "timeoutSeconds"

// longRunningCommands are manifest command paths whose synchronous
// endpoint waits on cluster work and declares no `timeoutSeconds`.
// Keyed on the exact manifest path so a rename surfaces as a lost
// entry rather than a silently wrong match.
// A command that answers with a jobId is deliberately absent: the CALL
// returns as soon as the job is queued, and the job is followed, not
// waited on. That is why nodes/drain, storage-groups/add-device,
// remove-device and remove-node are not here.
var longRunningCommands = map[string]bool{
	"vms/nvlink-check":   true,
	"vms/rotate-ssh-key": true,
	"vms/screenshot":     true,
	// Review 2 item 8. vms/migrate is synchronous and reads live cluster
	// state before it accepts the move: the VM's real state from the host,
	// then the LINSTOR pin. On a slow or degraded cluster that ran past
	// 30 s, and the caller was told the migration failed while conductor
	// went on to accept it.
	"vms/migrate":              true,
	"virt/reapply":             true,
	"vm-groups/reapply-policy": true,
	// storage-groups/delete removes each replica of the pool from LINSTOR
	// per node and answers with the result. No jobId, so the client waits.
	"storage-groups/delete":         true,
	"storage-groups/wipe-device":    true,
	"storage-groups/evict-node":     true,
	"storage-groups/inspect-device": true,
}

// For returns the HTTP deadline for one dispatch of cmdDef.
//
// Priority: the budget the caller actually asked for (body
// `timeoutSeconds`), then the manifest's declared default for that
// field, then the long-running catalogue, then Default. A declared
// budget always wins over the catalogue, because the caller named it.
func For(cmdDef manifest.Command, body map[string]any) time.Duration {
	if secs, ok := timeoutSeconds(body); ok {
		return budget(secs)
	}
	if cmdDef.Input != nil {
		for _, field := range cmdDef.Input.Fields {
			if field.Name != timeoutFieldName {
				continue
			}
			if secs, ok := asSeconds(field.Default); ok {
				return budget(secs)
			}
			// The field exists but carries no default: conductor's
			// documented ceiling is 600 s, so allow the whole range
			// rather than guess a smaller one.
			return LongRunning
		}
	}
	if longRunningCommands[cmdDef.Command] {
		return LongRunning
	}
	return Default
}

// budget converts a server-side second count into a client deadline,
// never returning less than Default.
func budget(seconds int64) time.Duration {
	d := time.Duration(seconds)*time.Second + headroom
	if d < Default {
		return Default
	}
	return d
}

// timeoutSeconds reads the caller's declared budget out of the request
// body, normalising the int / int64 / float64 shapes the CLI and MCP
// paths deposit.
func timeoutSeconds(body map[string]any) (int64, bool) {
	if body == nil {
		return 0, false
	}
	return asSeconds(body[timeoutFieldName])
}

// asSeconds normalises a manifest default or body value to a positive
// second count.
func asSeconds(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return int64(n), true
		}
	case int64:
		if n > 0 {
			return n, true
		}
	case float64:
		if n > 0 {
			return int64(n), true
		}
	}
	return 0, false
}
