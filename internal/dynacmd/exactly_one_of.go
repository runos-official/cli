package dynacmd

import (
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// exactlyOneOfFields returns the field names a command requires EXACTLY
// ONE of, for commands whose manifest cannot say so.
//
// The manifest field schema has `required`, which is per field, and no
// way to express "one of these two". `storage-groups/evict-node` takes
// `nid` OR `hostname`, so both are declared optional; the CLI then sent a
// body with neither, the destructive prompt read "(no target given)", and
// the call still went to the wire. Regression target: goal 21 B12.
//
// Kept as an explicit allow-list rather than inferred from the
// descriptions, because guessing a mutual-exclusion rule out of prose
// would refuse valid calls the moment a description is reworded.
func exactlyOneOfFields(command string) []string {
	switch command {
	case "storage-groups/evict-node":
		return []string{"nid", "hostname"}
	}
	return nil
}

// refuseUnlessExactlyOne enforces exactlyOneOfFields against a collected
// body. Returns nil for commands with no such rule.
func refuseUnlessExactlyOne(cmdDef manifest.Command, body map[string]any) error {
	names := exactlyOneOfFields(cmdDef.Command)
	if len(names) == 0 {
		return nil
	}
	var given []string
	for _, name := range names {
		if v, ok := body[name]; ok && !isEmptyString(v) && v != nil {
			given = append(given, name)
		}
	}
	if len(given) == 1 {
		return nil
	}
	flags := make([]string, 0, len(names))
	for _, name := range names {
		flags = append(flags, "--"+flagNameFor(name))
	}
	if len(given) == 0 {
		return fmt.Errorf("%s needs a target: pass exactly one of %s", lastPathSegment(cmdDef.Command), strings.Join(flags, " or "))
	}
	return fmt.Errorf("%s takes exactly one of %s, and %d were given: drop one", lastPathSegment(cmdDef.Command), strings.Join(flags, " or "), len(given))
}

// integerBodyValue normalises the int / int64 / float64 shapes an
// integer field can land in (cobra deposits int, a -f YAML file
// deposits int, MCP JSON deposits float64).
func integerBodyValue(body map[string]any, name string) (int64, bool) {
	switch n := body[name].(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	return 0, false
}
