package dynacmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// `vm-images capture` and `tools domain-check` accept their primary field
// as a positional (A10 / I13-H), but the use line was built from the
// manifest's positional fields alone, so `runos vm-images capture --help`
// showed `capture` and the shape the CLI actually accepts was invisible.
func TestBuildUseLine_ShowsConveniencePositionals(t *testing.T) {
	b := &Builder{}
	cases := []struct {
		name    string
		cmdDef  manifest.Command
		useName string
		want    string
	}{
		{
			name: "vm-images capture",
			cmdDef: manifest.Command{
				Command: "vm-images/capture",
				Input:   &manifest.Input{Fields: []manifest.Field{{Name: "vmid", Type: "string", Required: true}}},
			},
			useName: "capture",
			want:    "[vmid]",
		},
		{
			name: "tools domain-check",
			cmdDef: manifest.Command{
				Command: "tools/domain-check",
				Input:   &manifest.Input{Fields: []manifest.Field{{Name: "domain", Type: "string", Required: true}}},
			},
			useName: "domain-check",
			want:    "[domain]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := b.buildUseLine(c.useName, c.cmdDef)
			if !strings.Contains(got, c.want) {
				t.Errorf("buildUseLine(%q) = %q, want it to name %s", c.useName, got, c.want)
			}
		})
	}
}

// A field that is already positional must not be repeated by the
// convenience pass.
func TestBuildUseLine_NoDuplicateSlot(t *testing.T) {
	b := &Builder{}
	cmdDef := manifest.Command{
		Command: "vm-images/capture",
		Input:   &manifest.Input{Fields: []manifest.Field{{Name: "vmid", Type: "string", Required: true, Positional: true}}},
	}
	got := b.buildUseLine("capture", cmdDef)
	if strings.Count(got, "vmid") != 1 {
		t.Errorf("buildUseLine = %q, want vmid named once", got)
	}
}
