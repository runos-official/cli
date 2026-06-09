package dynacmd

import (
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// buildKitAdvancedFields mirrors the input.fields of the conductor
// manifest command services/buildkit/{id}/set-advanced-configs (shipped
// in MANIFEST_VERSION 28.24.0, objective 60 story 108). The CLI surface
// for this command is fully manifest-driven: no static cobra code, no
// MCP shim. These tests pin that the generic dynacmd synthesizer turns
// this field set into the expected flag surface so a conductor-side
// manifest bump can't silently regress the CLI/MCP contract.
//
// Types match the conductor schema: gc is the on/off toggle (boolean);
// snapshotter/logLevel/logFormat + platforms are string; everything else
// is integer. The flat key->value wire body uses the camelCase field
// names verbatim (the kebab flag is a CLI-surface alias only).
func buildKitAdvancedFields() []manifest.Field {
	return []manifest.Field{
		{Name: "id", Type: "string", Required: true, Positional: true},
		{Name: "gc", Type: "boolean"},
		{Name: "gcMaxUsedSpaceGb", Type: "integer"},
		{Name: "gcReservedSpaceGb", Type: "integer"},
		{Name: "gcMinFreeSpaceGb", Type: "integer"},
		{Name: "gcKeepDurationShortH", Type: "integer"},
		{Name: "gcKeepBytesShortGb", Type: "integer"},
		{Name: "gcKeepDurationLongH", Type: "integer"},
		{Name: "gcKeepBytesLongGb", Type: "integer"},
		{Name: "buildMaxParallelism", Type: "integer"},
		{Name: "cniPoolSize", Type: "integer"},
		{Name: "historyMaxAgeS", Type: "integer"},
		{Name: "historyMaxEntries", Type: "integer"},
		{Name: "platforms", Type: "string"},
		{Name: "snapshotter", Type: "string"},
		{Name: "logLevel", Type: "string"},
		{Name: "logFormat", Type: "string"},
		{Name: "platformsCacheMaxAgeS", Type: "integer"},
	}
}

// buildKitSetAdvancedConfigsCommand is the full manifest command shape
// (endpoint + jobId output) so the helper tests below can exercise the
// gates that drive `-f` and `--follow` registration.
func buildKitSetAdvancedConfigsCommand() manifest.Command {
	return manifest.Command{
		Command:  "services/buildkit/{id}/set-advanced-configs",
		Endpoint: "/:aid/:cid/services/buildkit/:id/set-advanced-configs",
		Method:   "POST",
		MCP:      []string{"write"},
		Input:    &manifest.Input{Fields: buildKitAdvancedFields()},
		Output:   &manifest.Output{Type: "object", Fields: []manifest.OutputField{{Name: "jobId"}}},
	}
}

// TestBuildKitAdvancedFlagMapping pins every BuildKit advanced-config
// field name -> kebab flag spelling, the canonical regression target for
// this story (mirrors the clone-database mapping test). All 17 knobs
// kebab cleanly: none carries a consecutive-uppercase acronym, so none
// needs a flagSpellingOverrides entry, and the natural transform is the
// agreed flag form. If conductor renames a field or the kebab transform
// drifts, this fails loudly instead of shipping an unguessable flag.
func TestBuildKitAdvancedFlagMapping(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"gc", "gc"},
		{"gcMaxUsedSpaceGb", "gc-max-used-space-gb"},
		{"gcReservedSpaceGb", "gc-reserved-space-gb"},
		{"gcMinFreeSpaceGb", "gc-min-free-space-gb"},
		{"gcKeepDurationShortH", "gc-keep-duration-short-h"},
		{"gcKeepBytesShortGb", "gc-keep-bytes-short-gb"},
		{"gcKeepDurationLongH", "gc-keep-duration-long-h"},
		{"gcKeepBytesLongGb", "gc-keep-bytes-long-gb"},
		{"buildMaxParallelism", "build-max-parallelism"},
		{"cniPoolSize", "cni-pool-size"},
		{"historyMaxAgeS", "history-max-age-s"},
		{"historyMaxEntries", "history-max-entries"},
		{"platforms", "platforms"},
		{"snapshotter", "snapshotter"},
		{"logLevel", "log-level"},
		{"logFormat", "log-format"},
		{"platformsCacheMaxAgeS", "platforms-cache-max-age-s"},
	}
	for _, tt := range cases {
		t.Run(tt.field, func(t *testing.T) {
			if got := flagNameFor(tt.field); got != tt.want {
				t.Errorf("flagNameFor(%q) = %q, want %q", tt.field, got, tt.want)
			}
		})
	}
}

// TestBuildKitAdvancedNoFlagCollisions guards the I-class invariant that
// no two BuildKit fields collapse to the same kebab flag (which would
// silently shadow one knob behind another via cobra last-registration-
// wins). Runs over the live field set so adding a field to the fixture
// can't reintroduce a clash unnoticed.
func TestBuildKitAdvancedNoFlagCollisions(t *testing.T) {
	seen := map[string]string{}
	for _, f := range buildKitAdvancedFields() {
		flag := flagNameFor(f.Name)
		if prev, ok := seen[flag]; ok {
			t.Errorf("flag %q produced by both %q and %q (collision)", flag, prev, f.Name)
		}
		seen[flag] = f.Name
	}
}

// TestBuildKitAdvancedFlagSynthesis confirms addFieldFlags registers a
// correctly-typed flag for every BuildKit field: the positional id gets
// a string flag form, the toggle gets a bool, the scalars get int/string
// per the manifest type. This is the per-field-flag half of the
// acceptance criteria (the surface the user types directly).
func TestBuildKitAdvancedFlagSynthesis(t *testing.T) {
	cmd := &cobra.Command{Use: "set-advanced-configs"}
	fields := buildKitAdvancedFields()
	addFieldFlags(cmd, fields, "services/buildkit/{id}/set-advanced-configs")

	wantType := map[string]string{
		"id":                    "string",
		"gc":                    "bool",
		"gcMaxUsedSpaceGb":      "int",
		"gcReservedSpaceGb":     "int",
		"gcMinFreeSpaceGb":      "int",
		"gcKeepDurationShortH":  "int",
		"gcKeepBytesShortGb":    "int",
		"gcKeepDurationLongH":   "int",
		"gcKeepBytesLongGb":     "int",
		"buildMaxParallelism":   "int",
		"cniPoolSize":           "int",
		"historyMaxAgeS":        "int",
		"historyMaxEntries":     "int",
		"platforms":             "string",
		"snapshotter":           "string",
		"logLevel":              "string",
		"logFormat":             "string",
		"platformsCacheMaxAgeS": "int",
	}
	for _, f := range fields {
		flagName := flagNameFor(f.Name)
		flag := cmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("field %q: flag --%s not registered", f.Name, flagName)
			continue
		}
		if flag.Value.Type() != wantType[f.Name] {
			t.Errorf("field %q: flag --%s type = %q, want %q", f.Name, flagName, flag.Value.Type(), wantType[f.Name])
		}
	}
}

// TestBuildKitAdvancedFollowAndFileGates pins the two surface gates the
// command depends on: the jobId output drives `--follow` registration
// (the command emits a job, per the job-following convention), and the
// presence of body input drives the `-f/--file` registration. Both are
// pure predicates so the wiring is verified without building the full
// leaf.
func TestBuildKitAdvancedFollowAndFileGates(t *testing.T) {
	cmd := buildKitSetAdvancedConfigsCommand()

	if !hasJobIdOutput(cmd) {
		t.Error("hasJobIdOutput = false; --follow would not be wired despite the jobId output")
	}
	if cmd.Input == nil {
		t.Error("Input is nil; -f/--file would not be registered")
	}
	if !hasNonPositionalInput(cmd) {
		t.Error("hasNonPositionalInput = false; the command would read as body-less despite 17 scalar knobs")
	}
}

// TestBuildKitAdvancedBodyCoercion confirms the `-f file.yaml` path lands
// each field on the wire under its camelCase name with the right JSON
// type: a YAML toggle stays bool, the GB/seconds scalars stay int, the
// enum/csv fields stay string. coerceBodyFileValue is the same helper
// that fixed foreman #40 (string-typed set-*-config fields); here we pin
// it for the BuildKit integer/boolean/string mix so a `-f` body and the
// equivalent per-field flags produce identical, correctly-typed bodies.
func TestBuildKitAdvancedBodyCoercion(t *testing.T) {
	cmd := buildKitSetAdvancedConfigsCommand()
	types := bodyFileFieldTypes(cmd)

	// Sanity: every non-positional field is registered for coercion.
	for _, f := range buildKitAdvancedFields() {
		if _, ok := types[f.Name]; !ok {
			t.Errorf("bodyFileFieldTypes missing %q", f.Name)
		}
	}

	cases := []struct {
		field string
		in    any
		want  any
	}{
		// YAML parses these as their native scalar types; coercion is a
		// no-op pass-through that preserves the wire type.
		{"gc", false, false},
		{"buildMaxParallelism", 8, 8},
		{"historyMaxAgeS", 172800, 172800},
		{"snapshotter", "overlayfs", "overlayfs"},
		{"logLevel", "debug", "debug"},
		{"platforms", "linux/amd64,linux/arm64", "linux/amd64,linux/arm64"},
		// Defensive: a YAML integral float on an integer field coerces
		// back to int (json/yaml number ambiguity) rather than reaching
		// the conductor as a float and failing validation.
		{"cniPoolSize", float64(16), 16},
		// A bool typed as a YAML string still coerces to bool.
		{"gc", "true", true},
	}
	for _, tt := range cases {
		t.Run(tt.field, func(t *testing.T) {
			got := coerceBodyFileValue(types[tt.field], tt.in)
			if got != tt.want {
				t.Errorf("coerceBodyFileValue(%q=%v) = %v (%T), want %v (%T)", tt.field, tt.in, got, got, tt.want, tt.want)
			}
		})
	}
}
