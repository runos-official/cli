package services

import (
	"fmt"
	"sort"

	"github.com/runos-official/cli/internal/manifest"
)

// Clearable fields: the yaml file is desired state again.
//
// Before conductor manifest 48.26.0 a partial update that OMITTED a
// per-type "clearable" key cleared the stored value, so `services sync`
// could express "the operator deleted this line from the file" by simply
// not sending the key. That rule also made every unrelated one-flag
// update destructive: the node-affinity pin renders into the pod
// template, so lowering a replica count rewrote the template and the
// controller replaced every pod of a pinned lane.
//
// Conductor story 256 reversed it. Omitting a key now PRESERVES the
// stored value, and removal moved onto the field's explicit empty value,
// which is the only shape that can mean "removed" without also meaning
// "not mentioned". Without the code in this file, a key the operator
// deleted from the yaml would silently never be applied: `services diff`
// would keep reporting the pin as a difference and the PATCH body would
// have nothing in it that could resolve that difference.
//
// THE CLI HOLDS NO LIST. Which fields work that way is read from the
// deployed manifest's `clearable` marker, per service type, every run. A
// type whose manifest carries no marker gets no removal, which is how a
// surface that keeps omit-equals-clear stays correct without a carve-out
// here.
//
// The pattern is not new in this repository: `internal/apps/sync.go`
// always includes `requires` in the PATCH body, even when empty, so an
// operator who deleted every entry wipes the server's set. That is the
// exemplar this file applies to the services surface. The apps surface
// itself is deliberately unchanged and keeps omit-equals-clear (RunOS
// item 456).

// emptyValueFor returns the wire value that REMOVES a stored value for a
// field of the given declared type, and whether the CLI knows one.
//
// The set is closed on purpose. Conductor's marker names exactly two
// shapes, `[]` for an array field and `""` for a string field, and a
// guessed third shape is how this story could put a destructive value on
// the wire: `replicas` is declared `integer` on the same update command,
// and a zero replica count stops a lane. So an unrecognised declared
// type produces no wire entry at all and a refused line instead.
func emptyValueFor(declaredType string) (any, bool) {
	switch declaredType {
	case "array":
		return []any{}, true
	case "string":
		return "", true
	default:
		return nil, false
	}
}

// ClearableFields returns the fields of a type's update command that the
// deployed manifest marks clearable, keyed by field name.
//
// Positional fields are skipped for the same reason UpdateInputFieldNames
// skips them: the executor lifts those out of the body to substitute a
// path placeholder, so they are not body keys at all.
func ClearableFields(updateCmd *manifest.Command) map[string]manifest.Field {
	out := map[string]manifest.Field{}
	if updateCmd == nil || updateCmd.Input == nil {
		return out
	}
	for _, f := range updateCmd.Input.Fields {
		if f.Positional || !f.Clearable {
			continue
		}
		out[f.Name] = f
	}
	return out
}

// computeClearRemovals returns the explicit empty values the PATCH body
// must carry because the local yaml deleted a clearable key that the
// server still holds, plus a refused line for every marked field whose
// declared type has no empty shape the CLI can serialise.
//
// All four conditions have to hold before a field is removed:
//
//  1. the deployed manifest marks the field clearable on this type's
//     update command;
//  2. the local yaml OMITS the field. A key that is present, including
//     one present with a nil value, is not a deletion. Conductor's own
//     rule is that a bare `nodeAffinityTags:` still means "not
//     provided", and the CLI must not read it differently;
//  3. the server projection holds the field. A field the show command
//     does not return is absent from that projection, so a type whose
//     pin is write-only can never have a removal invented for it; and
//  4. the server value is not already the empty value, so a sync cannot
//     manufacture a no-op removal for a service that carries nothing.
//
// Conditions 3 and 4 together are what stops a freshly pulled file from
// inventing a removal for a field the server never held.
func computeClearRemovals(local, server map[string]any, updateCmd *manifest.Command) (map[string]any, []string) {
	marked := ClearableFields(updateCmd)
	if len(marked) == 0 || len(server) == 0 {
		return nil, nil
	}

	removals := map[string]any{}
	var refused []string
	for name, field := range marked {
		if _, present := local[name]; present {
			continue
		}
		stored, held := server[name]
		if !held || isEmptyStoredValue(stored) {
			continue
		}
		empty, known := emptyValueFor(field.Type)
		if !known {
			refused = append(refused, fmt.Sprintf(
				"%s: deleted from the yaml, but the manifest marks it clearable with declared type %q, which has no empty value the CLI knows how to send; the stored value is left in place. Report the type to the conductor manifest owner",
				name, field.Type))
			continue
		}
		removals[name] = empty
	}
	sort.Strings(refused)
	if len(removals) == 0 {
		removals = nil
	}
	return removals, refused
}

// isEmptyStoredValue reports whether the value the server holds is
// already the field's empty value, so that deleting the key from the
// yaml asks for nothing. An absent value decodes as nil, an emptied
// array as a zero-length slice and an emptied string as "".
func isEmptyStoredValue(v any) bool {
	switch stored := v.(type) {
	case nil:
		return true
	case string:
		return stored == ""
	case []any:
		return len(stored) == 0
	default:
		return false
	}
}

// removalNames returns the removed field names in a stable order, for
// the plan's `removals` list and the text plan's `removed` section.
func removalNames(removals map[string]any) []string {
	if len(removals) == 0 {
		return nil
	}
	names := make([]string, 0, len(removals))
	for name := range removals {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
