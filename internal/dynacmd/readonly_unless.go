package dynacmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// FCR 1909. Conductor marks a command that changes nothing unless one input
// field holds a listed value (manifest.ReadOnlyUnless). The watchdog probe is
// the first: it reads sysfs and /proc by default, and LOAD_DRIVER=true loads a
// kernel module. A read behind a destructive prompt trains operators to type
// --yes without reading, so the CLI and MCP honour the marker per call.
//
// The marker only NARROWS: it can clear the prompt for a call that
// IsDestructiveCommand gates, and it never gates a call that was not gated.
// IsDestructiveCommand still answers for the command as a whole, so --yes and
// the MCP confirm argument stay registered for the state-changing case.
//
// Every doubt fails closed (the call keeps its confirmation): a malformed
// marker, a field the command does not declare, a positional field, a value
// that is not a scalar, a body file that cannot be read, and a DELETE.

// ConditionalReadOnly returns the marker when this CLI can honour it for
// cmdDef. ok=false means every call of cmdDef that IsDestructiveCommand gates
// stays gated.
func ConditionalReadOnly(cmdDef manifest.Command) (rule manifest.ReadOnlyUnless, field manifest.Field, ok bool) {
	r := cmdDef.ReadOnlyUnless
	if r == nil || r.Malformed || r.Field == "" || len(r.Values) == 0 {
		return manifest.ReadOnlyUnless{}, manifest.Field{}, false
	}
	for _, v := range r.Values {
		if strings.TrimSpace(v) == "" {
			return manifest.ReadOnlyUnless{}, manifest.Field{}, false
		}
	}
	if strings.EqualFold(cmdDef.Method, "DELETE") || cmdDef.Input == nil {
		return manifest.ReadOnlyUnless{}, manifest.Field{}, false
	}
	for _, f := range cmdDef.Input.Fields {
		if f.Name != r.Field {
			continue
		}
		if f.Positional {
			return manifest.ReadOnlyUnless{}, manifest.Field{}, false
		}
		return *r, f, true
	}
	return manifest.ReadOnlyUnless{}, manifest.Field{}, false
}

// IsDestructiveInvocation answers IsDestructiveCommand for one call whose
// inputs are args, keyed by manifest field name (the MCP shape). An absent or
// null field takes the manifest default, which is what the server applies.
func IsDestructiveInvocation(cmdDef manifest.Command, args map[string]any) bool {
	if !IsDestructiveCommand(cmdDef) {
		return false
	}
	rule, field, ok := ConditionalReadOnly(cmdDef)
	if !ok {
		return true
	}
	if v, present := args[rule.Field]; present && v != nil {
		return valueTriggers(rule, v)
	}
	return defaultTriggers(rule, field)
}

// cliInvocationDestructive is IsDestructiveInvocation for a cobra call. The
// field can arrive by flag or by -f body file. When both carry it, the call
// is destructive if EITHER one triggers, so the answer never depends on which
// source the executor lets win.
func cliInvocationDestructive(c *cobra.Command, cmdDef manifest.Command) bool {
	rule, field, ok := ConditionalReadOnly(cmdDef)
	if !ok || c == nil {
		return true
	}
	seen := false
	if f := c.Flags().Lookup(flagNameFor(rule.Field)); f != nil && f.Changed {
		seen = true
		if valueTriggers(rule, f.Value.String()) {
			return true
		}
	}
	if f := c.Flags().Lookup("file"); f != nil && f.Value.String() != "" {
		body, err := loadYAMLFile(f.Value.String())
		if err != nil {
			return true
		}
		if v, present := body[rule.Field]; present && v != nil {
			seen = true
			if valueTriggers(rule, v) {
				return true
			}
		}
	}
	if seen {
		return false
	}
	return defaultTriggers(rule, field)
}

func defaultTriggers(rule manifest.ReadOnlyUnless, field manifest.Field) bool {
	if field.Default == nil {
		return false
	}
	return valueTriggers(rule, field.Default)
}

// valueTriggers reports whether v is one of the marker's values. Conductor
// compares String(value).toLowerCase(); this side also trims, so a padded
// " true" prompts (the safe direction). A value that is not a scalar fails
// closed, because String(["true"]) is "true" on the server.
func valueTriggers(rule manifest.ReadOnlyUnless, v any) bool {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		s = fmt.Sprint(x)
	default:
		return true
	}
	s = strings.TrimSpace(s)
	for _, want := range rule.Values {
		if strings.EqualFold(s, strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}
