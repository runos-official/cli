package dynacmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Review 2 item 19. The key=value refusal fired only when the manifest
// declared `valueType`. `requires` and `providerOptions` declare none,
// and their values are objects and booleans, so `--requires db=abc12`
// silently built {"db":"abc12"} and the server refused a body the CLI had
// invented. The MCP schema already carves both out; the flag parser reads
// the same carve-out now.
func TestParseObjectFlagValues_UndeclaredNonStringMapsDemandJSON(t *testing.T) {
	cases := []struct {
		flag  string
		field manifest.Field
		raw   []string
	}{
		{"requires", manifest.Field{Name: "requires", Type: "object"}, []string{"db=abc12"}},
		{"provider-options", manifest.Field{Name: "providerOptions", Type: "object"}, []string{"proxied=true"}},
	}
	for _, c := range cases {
		t.Run(c.flag, func(t *testing.T) {
			_, err := parseObjectFlagValues(c.flag, c.raw, c.field)
			if err == nil {
				t.Fatalf("--%s: expected a refusal, key=value cannot express this map", c.flag)
			}
			for _, want := range []string{"JSON", "--" + c.flag} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal should mention %q, got: %s", want, err.Error())
				}
			}
		})
	}
}

// The JSON shape still goes through for those fields: it is the shape
// that can express them.
func TestParseObjectFlagValues_JSONStillAcceptedForCarvedOutFields(t *testing.T) {
	got, err := parseObjectFlagValues("requires", []string{`{"db":{"id":"abc12","type":"postgresql"}}`}, manifest.Field{Name: "requires", Type: "object"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	inner, ok := got["db"].(map[string]any)
	if !ok || inner["id"] != "abc12" {
		t.Errorf("got %+v, want the nested object preserved", got)
	}
}

// An ordinary map of strings keeps the key=value shape.
func TestParseObjectFlagValues_StringMapStillTakesKeyValue(t *testing.T) {
	got, err := parseObjectFlagValues("labels", []string{"env=prod"}, manifest.Field{Name: "labels", Type: "object"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["env"] != "prod" {
		t.Errorf("got %+v, want env=prod", got)
	}
}

// Review 2 item 19, second half. The help text said "repeatable
// key=value, or one JSON object" on every object field, including the two
// that refuse key=value. Help that contradicts the refusal costs the
// caller a round trip to find out.
func TestObjectFlagUsage_SaysWhatTheFieldActuallyTakes(t *testing.T) {
	stringMap := objectFlagUsage(manifest.Field{Name: "labels", Type: "object"})
	if !strings.Contains(stringMap, "key=value") {
		t.Errorf("a string map must advertise key=value, got %q", stringMap)
	}
	for _, name := range []string{"requires", "providerOptions"} {
		usage := objectFlagUsage(manifest.Field{Name: name, Type: "object"})
		if strings.Contains(usage, "repeatable key=value") {
			t.Errorf("%s refuses key=value, so its help must not offer it, got %q", name, usage)
		}
		if !strings.Contains(usage, "JSON") {
			t.Errorf("%s help must name the JSON object shape, got %q", name, usage)
		}
	}
}
