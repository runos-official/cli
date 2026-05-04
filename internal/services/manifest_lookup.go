package services

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// Command name patterns. The manifest exposes per-type commands keyed by
// the literal string "services/<type>/{id}/show" (and friends). We never
// hard-code the type list in this package: ListSupportedTypes derives
// it from whichever types have all three commands present in the
// manifest, so a new conductor service type lights up automatically on
// the next `runos manifest update`.
const (
	commandPrefix    = "services/"
	commandShowSfx   = "/{id}/show"
	commandUpdateSfx = "/{id}/update"
	commandAddSfx    = "/add"
	commandDeleteSfx = "/{id}/delete"
	commandListSfx   = "/list"
)

// ShowCommand returns the manifest command for services/<type>/{id}/show.
// Returns an error if the type isn't known to the manifest.
func ShowCommand(m *manifest.Manifest, serviceType string) (*manifest.Command, error) {
	return findCommand(m, commandPrefix+serviceType+commandShowSfx)
}

// UpdateCommand returns the manifest command for services/<type>/{id}/update.
// As of the recent conductor change this is a PATCH; the lookup doesn't
// care about the verb so a future PUT/PATCH swap is invisible here.
func UpdateCommand(m *manifest.Manifest, serviceType string) (*manifest.Command, error) {
	return findCommand(m, commandPrefix+serviceType+commandUpdateSfx)
}

// AddCommand returns the manifest command for services/<type>/add.
func AddCommand(m *manifest.Manifest, serviceType string) (*manifest.Command, error) {
	return findCommand(m, commandPrefix+serviceType+commandAddSfx)
}

// DeleteCommand returns the manifest command for services/<type>/{id}/delete.
// Used by the 409-formatting wrapper around `services <type> delete`.
func DeleteCommand(m *manifest.Manifest, serviceType string) (*manifest.Command, error) {
	return findCommand(m, commandPrefix+serviceType+commandDeleteSfx)
}

// ListCommand returns the manifest command for services/<type>/list.
// Used by services_pull --all.
func ListCommand(m *manifest.Manifest, serviceType string) (*manifest.Command, error) {
	return findCommand(m, commandPrefix+serviceType+commandListSfx)
}

// ListSupportedTypes returns the set of service types that have a
// complete CRUD surface in the manifest (show + add + update). Used to
// validate a yaml's `type:` header against the live manifest and to
// drive `services pull --all` discovery.
func ListSupportedTypes(m *manifest.Manifest) []string {
	if m == nil {
		return nil
	}
	have := map[string]struct {
		show, add, update bool
	}{}
	for _, c := range m.Commands {
		t, suffix, ok := splitTypeSuffix(c.Command)
		if !ok {
			continue
		}
		entry := have[t]
		switch suffix {
		case commandShowSfx:
			entry.show = true
		case commandAddSfx:
			entry.add = true
		case commandUpdateSfx:
			entry.update = true
		}
		have[t] = entry
	}
	var types []string
	for t, entry := range have {
		if entry.show && entry.add && entry.update {
			types = append(types, t)
		}
	}
	sort.Strings(types)
	return types
}

// IsSupportedType reports whether the manifest has the full pull/diff/sync
// surface for the given type. Wraps ListSupportedTypes for callers that
// only need a yes/no answer for one type.
func IsSupportedType(m *manifest.Manifest, serviceType string) bool {
	return slices.Contains(ListSupportedTypes(m), serviceType)
}

// UpdateInputFieldNames returns the set of field names accepted by the
// services/<type>/{id}/update endpoint. services_sync's PATCH body is
// constructed by intersecting this set with the keys present in the local
// yaml: any other yaml key (typically read-only or immutable-after-create
// fields) is surfaced as a "refused" entry rather than silently dropped.
//
// Callers should call UpdateCommand first to get a friendly error when
// the type isn't supported; this helper assumes a non-nil command def.
func UpdateInputFieldNames(cmd *manifest.Command) map[string]bool {
	out := map[string]bool{}
	if cmd == nil || cmd.Input == nil {
		return out
	}
	for _, f := range cmd.Input.Fields {
		// Skip path-substitution fields (e.g. `id`); the executor pulls
		// those out of the body before sending. Everything else is
		// genuinely PATCHable.
		if f.Positional {
			continue
		}
		out[f.Name] = true
	}
	return out
}

// AddInputFieldNames returns the set of field names accepted by
// services/<type>/add. Used by services_sync when the local yaml has no
// id (creation flow).
func AddInputFieldNames(cmd *manifest.Command) map[string]bool {
	out := map[string]bool{}
	if cmd == nil || cmd.Input == nil {
		return out
	}
	for _, f := range cmd.Input.Fields {
		if f.Positional {
			continue
		}
		out[f.Name] = true
	}
	return out
}

// ShowOutputFields returns the ordered list of field names the manifest
// declares for services/<type>/{id}/show output. services_pull uses
// this to project the HTTP response into the on-disk yaml's Fields map,
// so adding a field on the API side flows through automatically.
func ShowOutputFields(cmd *manifest.Command) []string {
	if cmd == nil {
		return nil
	}
	return cmd.Output.FieldNames()
}

// findCommand iterates m.Commands looking for an exact command-name
// match. Returns a typed not-found error so callers can branch.
func findCommand(m *manifest.Manifest, name string) (*manifest.Command, error) {
	if m == nil {
		return nil, fmt.Errorf("manifest is nil")
	}
	for i := range m.Commands {
		if m.Commands[i].Command == name {
			return &m.Commands[i], nil
		}
	}
	return nil, fmt.Errorf("manifest has no command %q (run `runos manifest update`?)", name)
}

// splitTypeSuffix tears apart "services/<type>/<suffix>" into its parts.
// Returns (type, suffix-with-leading-slash, true) for a recognised
// services command; (_, _, false) otherwise.
func splitTypeSuffix(cmd string) (string, string, bool) {
	if !strings.HasPrefix(cmd, commandPrefix) {
		return "", "", false
	}
	rest := cmd[len(commandPrefix):]
	// Type is everything up to the next slash.
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return "", "", false
	}
	return rest[:slash], rest[slash:], true
}
