package apps

import (
	"slices"
	"strings"
)

// OmitClearFields lists the top-level dotted field names the
// conductor's PATCH /apps/:id endpoint treats as desired-state with
// omit-equals-clear semantics. A local yaml that omits any of these
// wipes the server-side value on push. Every other top-level field is
// omit-equals-preserve unless it matches a nested-path pattern in
// IsOmitClearPath (see below).
//
// Canonical source: see computeYAMLPatch + buildFullYAMLBody in this
// file. `domain` is included because conductor's reconciler removes
// the mapping (and any provider-managed DNS record) when local omits
// it, even though the actual delete happens via the domains/services
// orchestration rather than the apps PATCH. Surfacing it here keeps
// the pre-deploy "will be cleared" classification honest.
var OmitClearFields = []string{
	"healthCheck",
	"healthCheckPort",
	"healthCheckPath",
	"metricsPort",
	"metricsPath",
	"domain",
}

// IsOmitClearField reports whether the named top-level yaml field
// would be cleared on a PATCH that omits it. Matches against the
// top-level-only dotted-path summaries listServerOnlyFields produces.
func IsOmitClearField(name string) bool {
	return slices.Contains(OmitClearFields, name)
}

// IsOmitClearPath reports whether a full dotted-path summary (as
// emitted by listServerOnlyFields, e.g. "servicePortMappings[0].domains
// (1 entry)") describes a field with omit-equals-clear semantics. Used
// alongside IsOmitClearField to catch nested cases the top-level check
// misses, namely:
//
//   - servicePortMappings[N].domains : per-mapping custom-domain list,
//     same omit-deletes contract as top-level `domain:`. Without this
//     match the partition lumps a removed `servicePortMappings[].domains`
//     entry under "Preserved server-side" while the conductor goes
//     ahead and removes the mapping.
//
// The function is intentionally pattern-matched rather than regex-
// driven so future additions stay surveyable.
func IsOmitClearPath(path string) bool {
	if isStandardHttpsPath(path) {
		return true
	}
	// servicePortMappings[N].domains{,.<sub>} — any path that walks
	// through a mapping's `domains` array.
	if strings.HasPrefix(path, "servicePortMappings[") {
		if i := strings.IndexByte(path, ']'); i >= 0 && i+1 < len(path) {
			rest := path[i+1:]
			// rest looks like ".domains" or ".domains[0].fqdn (...)".
			if strings.HasPrefix(rest, ".domains") {
				return true
			}
		}
	}
	return false
}

// PartitionServerOnlyByClearSemantics splits a list of server-only
// fields (the output of listServerOnlyFields) into the two buckets the
// pre-deploy warning needs: clearOnOmit (will be wiped on push) vs
// preserveOnOmit (left alone server-side). Order within each bucket
// preserves the input order so the warning's bulleted list still
// matches the diff's display order.
//
// A field is classified clearOnOmit when EITHER its top-level name is
// in OmitClearFields OR its full path matches IsOmitClearPath.
func PartitionServerOnlyByClearSemantics(serverOnly []string) (clearOnOmit, preserveOnOmit []string) {
	for _, f := range serverOnly {
		topLevel := f
		if i := strings.IndexAny(f, ". ["); i >= 0 {
			topLevel = f[:i]
		}
		if IsOmitClearField(topLevel) || IsOmitClearPath(f) {
			clearOnOmit = append(clearOnOmit, f)
		} else {
			preserveOnOmit = append(preserveOnOmit, f)
		}
	}
	return
}

// IsOrchestrationRemovedPath reports whether a server-only field path
// (as produced by listServerOnlyFields) describes state the conductor's
// deploy orchestration removes via a step OTHER than the apps PATCH —
// so the pre-deploy gate's "Preserved server-side / WILL be cleared"
// classification doesn't apply.
//
// Currently the only entry is the `requires` block: the deploy
// orchestration's `replaceDependencies` step runs unconditionally on
// the new edge set, so a `requires.<alias>` the local yaml dropped is
// removed regardless of the apps PATCH's omit semantics. Pre-fix
// (I4-F R1 retest), the gate listed it under "Preserved server-side
// (no action needed)", which contradicted what the user just chose to
// do (intentional removal) and disagreed with the post-deploy state.
//
// As of conductor iter-4 R2, the same orchestration also strips the
// platform-injected secret env keys claimed by removed requires
// entries (I4-C/E full fix), so the requires-removed scenario has no
// orphan side effects to warn about either. Filtering the entries out
// here removes the misleading message entirely.
//
// Pure pattern matcher; no regex.
func IsOrchestrationRemovedPath(path string) bool {
	if path == "requires" || strings.HasPrefix(path, "requires.") || strings.HasPrefix(path, "requires (") {
		return true
	}
	return false
}

// FilterOrchestrationRemoved drops every entry classified as
// "removed by orchestration, not by the apps PATCH" from a list of
// server-only field paths. Used by the deploy gate's
// emitDeletionWarning so the bulleted summary only mentions
// PATCH-relevant fields, the ones whose presence/absence on the
// next push really determines server state. Returns a freshly
// allocated slice; never mutates the input.
func FilterOrchestrationRemoved(serverOnly []string) []string {
	if len(serverOnly) == 0 {
		return nil
	}
	out := make([]string, 0, len(serverOnly))
	for _, f := range serverOnly {
		topLevel := f
		if i := strings.IndexAny(f, ". ["); i >= 0 {
			topLevel = f[:i]
		}
		if IsOrchestrationRemovedPath(topLevel) || IsOrchestrationRemovedPath(f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// splitServerOnlyEntry breaks a listServerOnlyFields entry like
// `cpuRequestMc (0)` into (name="cpuRequestMc", summary="0"). Returns
// the input unchanged as name with empty summary when the trailing
// "(...)" wrapper is missing, so callers don't need to special-case
// pre-summary entries.
func splitServerOnlyEntry(entry string) (name, summary string) {
	if i := strings.LastIndex(entry, " ("); i >= 0 && strings.HasSuffix(entry, ")") {
		return entry[:i], entry[i+2 : len(entry)-1]
	}
	return entry, ""
}

// isZeroValueSummary recognises the value-summary strings
// summarizeValue emits for zero-defaulted yaml values. Used by
// IsBenignPreserveZero; pinned to the canonical summary shapes so a
// future formatter change won't silently inflate the benign set.
func isZeroValueSummary(summary string) bool {
	switch summary {
	case "0", `""`, "false", "null", "0 entries", "0 fields":
		return true
	}
	return false
}

// IsBenignPreserveZero reports whether a server-only field entry
// (as emitted by listServerOnlyFields, e.g. `cpuRequestMc (0)` or
// `memoryRequestMb (0)`) has preserve-on-omit semantics AND a
// zero-default value summary. For these, omitting the field from the
// local yaml leaves server state unchanged, so the pre-deploy drift
// gate can wave the deploy through without `--force`.
//
// I3-E retest follow-up: a freshly-provisioned app emits zero-valued
// resource fields (cpuRequestMc, memoryRequestMb, etc.) even when the
// user has nothing customised locally; pre-fix the gate refused on
// every second deploy until the user pulled. The classification is
// conservative — clearOnOmit fields are never benign (their absence
// removes server state, which is meaningful), and non-zero values are
// never benign (a non-zero value the user hasn't pulled may be a
// customisation they'd want to round-trip rather than silently push
// over with their local omission).
func IsBenignPreserveZero(entry string) bool {
	name, summary := splitServerOnlyEntry(entry)
	if name == "" {
		return false
	}
	topLevel := name
	if i := strings.IndexAny(name, ".["); i >= 0 {
		topLevel = name[:i]
	}
	if IsOmitClearField(topLevel) || IsOmitClearPath(name) {
		return false
	}
	return isZeroValueSummary(summary)
}

// AnyServerOnlyIsBlocking returns true when at least one entry in
// the list either has clearOnOmit semantics or a non-zero
// preserve-on-omit value. Used by NeedsForceToDeploy to skip the
// hard refusal when every server-only field is a benign zero
// preserve-on-omit default (the I3-E retest case).
//
// An empty list reports false (no blocking entries) so a degenerate
// drift with no server-only fields doesn't accidentally trip the
// gate via this path; the caller's other refusal conditions still
// apply.
func AnyServerOnlyIsBlocking(entries []string) bool {
	for _, e := range entries {
		if !IsBenignPreserveZero(e) {
			return true
		}
	}
	return false
}
