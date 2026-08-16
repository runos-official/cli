package dynacmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// parseObjectFlagValues turns the raw `[]string` collected from a
// repeatable object-field flag into the map the wire body wants.
//
// An `object`-typed manifest field used to get no flag at all, so
// `labels` on vms create / update / restore, and every other map-shaped
// field, was reachable only through `-f body.yaml`. An agent reading the
// help saw the field advertised with no way to set it. Regression
// target: goal 19 A9 / goal 21 B10.
//
// Two accepted shapes:
//
//   - repeated `--<flag> key=value` (the common case; the value keeps
//     every `=` after the first, so base64 and URLs survive),
//   - one `--<flag> '{"k":"v"}'` JSON object, which is the only shape
//     that can express a non-string value.
//
// Returns nil for no values so the caller omits the field rather than
// sending an empty object, which for a clearable field means something
// different from "leave it alone".
func parseObjectFlagValues(flagName string, raw []string, field manifest.Field) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// A single element that parses as a JSON object is taken whole. This
	// is the only route for a map whose values are not strings.
	if len(raw) == 1 {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw[0]), &decoded); err == nil {
			return decoded, nil
		}
	}
	// A map whose values are objects or arrays cannot be expressed as
	// key=value, so say so instead of building a map of strings the
	// server will refuse.
	if field.ValueType != "" && field.ValueType != "string" {
		return nil, fmt.Errorf("--%s: the values of this map are %s, not strings, so key=value cannot express them. Pass one JSON object, e.g. --%s '{\"alias\":{...}}', or put the whole body in a YAML file and pass -f <file>.", flagName, field.ValueType, flagName)
	}
	out := make(map[string]any, len(raw))
	for _, elem := range raw {
		key, value, ok := strings.Cut(elem, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--%s: %q is not a key=value pair. Repeat the flag once per entry, e.g. --%s env=prod --%s team=core, or pass one JSON object --%s '{\"env\":\"prod\"}', or put the whole body in a YAML file and pass -f <file>.", flagName, elem, flagName, flagName, flagName)
		}
		out[key] = value
	}
	return out, nil
}

// objectFlagUsageSuffix is appended to an object field's help text so
// the accepted shapes are visible at flag-discovery time rather than
// after a refusal.
const objectFlagUsageSuffix = " (repeatable key=value, or one JSON object)"
