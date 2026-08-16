package apitimeout

import (
	"testing"
	"time"

	"github.com/runos-official/cli/internal/manifest"
)

// Goal 19 A4: a 30 s client-wide timeout cut off synchronous endpoints
// that conductor lets run for up to 600 s. The deadline is now derived
// per call, from the budget the caller asked for.
func TestFor(t *testing.T) {
	runCommand := manifest.Command{
		Command: "vms/run-command",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "command", Type: "string", Required: true},
			{Name: "timeoutSeconds", Type: "integer"},
		}},
	}
	execSQL := manifest.Command{
		Command: "services/postgresql/{id}/exec-sql",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "timeoutSeconds", Type: "integer", Default: float64(60)},
		}},
	}
	plainGet := manifest.Command{
		Command: "vms/list",
		Input:   &manifest.Input{Fields: []manifest.Field{{Name: "gid", Type: "string"}}},
	}

	cases := []struct {
		name   string
		cmdDef manifest.Command
		body   map[string]any
		want   time.Duration
	}{
		{"caller asks for the 600 s ceiling", runCommand, map[string]any{"timeoutSeconds": 600}, 660 * time.Second},
		{"caller asks for 120 s", runCommand, map[string]any{"timeoutSeconds": 120}, 180 * time.Second},
		{"float64 body value (MCP JSON shape)", runCommand, map[string]any{"timeoutSeconds": float64(300)}, 360 * time.Second},
		{"no explicit budget on a timeout-capable command", runCommand, nil, LongRunning},
		{"manifest default is honoured", execSQL, nil, 120 * time.Second},
		{"explicit budget beats the manifest default", execSQL, map[string]any{"timeoutSeconds": 300}, 360 * time.Second},
		{"ordinary read keeps the 30 s default", plainGet, nil, Default},
		{"long-running catalogue entry", manifest.Command{Command: "vms/rotate-ssh-key"}, nil, LongRunning},
		{"another catalogue entry", manifest.Command{Command: "storage-groups/wipe-device"}, nil, LongRunning},
		{"a tiny budget still gets the headroom", runCommand, map[string]any{"timeoutSeconds": 1}, 61 * time.Second},
		{"a zero budget is ignored", runCommand, map[string]any{"timeoutSeconds": 0}, LongRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := For(c.cmdDef, c.body); got != c.want {
				t.Errorf("For(%q) = %v, want %v", c.cmdDef.Command, got, c.want)
			}
		})
	}
}
