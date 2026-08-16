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
	// A map whose values are not strings cannot be expressed as
	// key=value, so say so instead of building a map of strings the
	// server will refuse.
	if shape := objectValueShape(field); shape != "" {
		return nil, fmt.Errorf("--%s: the values of this map are %s, and key=value can only send strings. Pass one JSON object, e.g. --%s '%s', or put the whole body in a YAML file and pass -f <file>.", flagName, shape, flagName, objectValueExample(field))
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

// undeclaredNonStringObjectFields names the object fields whose map
// VALUES are not strings although their manifest entry declares no
// `valueType`, with the shape to name in a refusal.
//
// The MCP tool schema carries the same carve-out in
// internal/mcp/server.go:projectObjectValue (providerOptions drops the
// additionalProperties constraint so booleans round-trip; requires is
// projected as map[alias] -> object). The flag parser did not, so
// `--requires db=abc12` and `--provider-options proxied=true` built a map
// of strings and the server refused a body the CLI had invented (review 2
// item 19). Both entries retire together the moment conductor's manifest
// declares valueType for them.
var undeclaredNonStringObjectFields = map[string]objectValueHint{
	"requires":        {shape: "objects", example: `{"db":{"id":"abc12","type":"postgresql"}}`},
	"providerOptions": {shape: "booleans and numbers as well as strings", example: `{"proxied":true}`},
}

// objectValueHint is what to tell the caller about a map field whose
// values key=value cannot carry: what the values are, and one line they
// can copy.
type objectValueHint struct {
	shape   string
	example string
}

// objectValueShape names what an object field's map values are when they
// are not plain strings, and returns "" when key=value can express them.
func objectValueShape(field manifest.Field) string {
	if field.ValueType != "" {
		if field.ValueType == "string" {
			return ""
		}
		return field.ValueType + "s"
	}
	return undeclaredNonStringObjectFields[field.Name].shape
}

// objectValueExample returns a copyable JSON object for the refusal.
func objectValueExample(field manifest.Field) string {
	if hint, ok := undeclaredNonStringObjectFields[field.Name]; ok && hint.example != "" {
		return hint.example
	}
	if field.ValueType == "object" {
		return `{"key":{...}}`
	}
	return `{"key":<value>}`
}

// objectFlagUsage is appended to an object field's help text so the
// accepted shapes are visible at flag-discovery time rather than after a
// refusal.
//
// It has to match what parseObjectFlagValues accepts: every object field
// used to advertise "repeatable key=value, or one JSON object" including
// the ones that refuse key=value, so the help contradicted the refusal
// (review 2 item 19).
func objectFlagUsage(field manifest.Field) string {
	if shape := objectValueShape(field); shape != "" {
		return " (one JSON object: the values are " + shape + ", which key=value cannot express)"
	}
	return " (repeatable key=value, or one JSON object)"
}
