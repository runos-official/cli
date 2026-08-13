package dynacmd

import (
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// Rendering the constraints the manifest already carries into `--help` (goal 21, O1).
//
// THE DEFECT. `runos nodes join-command --help` showed:
//
//	--format string   Command format for the target environment
//
// while the manifest declared `enum: ['ssh-local', 'ssh-remote', 'aws-cli']`. The data was there and
// the help renderer dropped it, so the only way to learn the allowed values was to send a wrong one
// and read the refusal.
//
// WHY IT IS WORSE FOR AN AGENT. That is one wasted round trip per enum field, every time. A person
// can open the source; an agent guesses and burns a call. This is the highest-leverage fix in the
// goal because it is one change that improves EVERY enum field across the whole surface at once.
//
// Pure. No I/O.

// Beyond this many values, listing them all turns a one-line flag description into a wall.
// The count is still reported so the reader knows the list is truncated rather than short.
const maxEnumValuesShown = 8

// describeConstraints renders the manifest's own constraints as a help suffix.
//
// Returns "" when the field declares none, so an unconstrained flag reads exactly as before.
// The leading space is included so callers can append unconditionally.
func describeConstraints(field manifest.Field) string {
	var parts []string

	if len(field.Enum) > 0 {
		parts = append(parts, describeEnum(field.Enum))
	}
	// Reported separately from the enum: a field can be a free string in a named shape
	// (`key_value`, `duration`) with no fixed set of values, and that shape is exactly what a
	// caller gets wrong.
	if field.Format != "" {
		parts = append(parts, fmt.Sprintf("format: %s", field.Format))
	}
	if def := describeDefault(field); def != "" {
		parts = append(parts, def)
	}

	// A REQUIRED BOOLEAN'S FALSE FORM IS INVISIBLE OTHERWISE (goal 21, O21). Cobra renders a bool
	// as a bare `--flag` with no `=value` hint, so presence reads as the only thing it can express
	// and the false case looks impossible. That is not an edge: on `nodes/configure-virt-shape`
	// it is half the command's purpose, since `vmHost` is required with no default precisely so
	// the absence of a choice cannot be mistaken for one.
	//
	// Only for REQUIRED booleans. An optional bool defaults sensibly and does not need the noise.
	if field.Type == "boolean" && field.Required {
		parts = append(parts, "pass =true or =false")
	}

	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

func describeEnum(values []string) string {
	if len(values) <= maxEnumValuesShown {
		return "one of: " + strings.Join(values, ", ")
	}
	shown := values[:maxEnumValuesShown]
	return fmt.Sprintf("one of %d, including: %s", len(values), strings.Join(shown, ", "))
}

// describeDefault renders a non-zero default.
//
// A zero default is deliberately NOT rendered. Cobra already prints `(default ...)` for non-zero
// values itself, and "default: false" or "default: 0" on every untouched flag is noise that pushes
// the real constraint off the line.
func describeDefault(field manifest.Field) string {
	if field.Default == nil {
		return ""
	}
	switch v := field.Default.(type) {
	case string:
		if v == "" {
			return ""
		}
		return fmt.Sprintf("default: %s", v)
	case bool:
		if !v {
			return ""
		}
		return "default: true"
	case float64:
		if v == 0 {
			return ""
		}
		// Manifest numbers arrive as float64 through JSON. Render whole numbers without the
		// trailing ".0" a naive %v would produce.
		if v == float64(int64(v)) {
			return fmt.Sprintf("default: %d", int64(v))
		}
		return fmt.Sprintf("default: %g", v)
	case int:
		if v == 0 {
			return ""
		}
		return fmt.Sprintf("default: %d", v)
	default:
		return ""
	}
}
