package output

import (
	"fmt"
	"sort"
	"strings"
)

// Nested-object rendering: the rules that turn one nested map into a single
// inline string for a table cell or an inline array join. Split out of
// formatter.go (objective 93, story 217) so the summary, containment and
// ordering rules are one concern in one file rather than more weight on the
// plain-text renderer, which already carries array tables, object blocks,
// sub-tables, cell truncation and field aliasing.

// summarisableBy reports whether every key of obj is one of `keys`,
// i.e. whether a pattern branch whose summary consumes exactly `keys`
// can describe obj WITHOUT DISCARDING ANYTHING.
//
// Objective 93 / story 217. formatNestedObject's pattern branches used
// to fire on the mere PRESENCE of their trigger key and then build a
// summary out of that key alone, so every other key in the object was
// silently dropped. The `state` branch is the one that mattered: every
// RunOS status sub-object carries `state`, so a status enriched with
// the failing backing service's name, type or reason had exactly that
// attribution eaten in the one place an operator scanning a list would
// look for it.
//
// Containment fixes the gate rather than the summary. An object the
// branch already described in full still takes the branch and renders
// byte-identically; anything richer falls through to the default
// alphabetised `k=v` branch below, which is lossless. One rule, used by
// all three branches, so there is no second copy to drift.
func summarisableBy(obj map[string]any, keys ...string) bool {
	for k := range obj {
		known := false
		for _, allowed := range keys {
			if k == allowed {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}

// formatNestedObject formats a nested object intelligently
func formatNestedObject(obj map[string]any, indent int) string {
	if len(obj) == 0 {
		return ""
	}

	// Check for common patterns and format them nicely

	// Pattern: network access entry (has link, name)
	if link, hasLink := obj["link"]; hasLink && summarisableBy(obj, "link", "name") {
		name, hasName := obj["name"]
		if hasName {
			return fmt.Sprintf("%s: %s", formatValueWithIndent(name, indent), formatValueWithIndent(link, indent))
		}
		return formatValueWithIndent(link, indent)
	}

	// Pattern: status object (has state, message)
	if state, hasState := obj["state"]; hasState && summarisableBy(obj, "state", "message") {
		msg, hasMsg := obj["message"]
		if hasMsg && msg != "" {
			return fmt.Sprintf("%s (%s)", formatValueWithIndent(state, indent), formatValueWithIndent(msg, indent))
		}
		return formatValueWithIndent(state, indent)
	}

	// Pattern: replicas info (has desired, ready, available)
	if desired, hasDesired := obj["desired"]; hasDesired && summarisableBy(obj, "desired", "ready", "available") {
		ready, hasReady := obj["ready"]
		available, hasAvailable := obj["available"]
		if hasReady && hasAvailable {
			return fmt.Sprintf("%v/%v ready, %v available",
				formatValueWithIndent(ready, indent),
				formatValueWithIndent(desired, indent),
				formatValueWithIndent(available, indent))
		}
	}

	// Default: show all keys as key=value pairs inline. The keys the
	// summary patterns above consume lead, in the order those summaries
	// put them; every remaining key follows alphabetised.
	//
	// ALPHABETISED, because pre-fix this branch iterated
	// `for k := range obj`, which is Go-spec non-deterministic, making
	// `agents list` render different field orders on consecutive
	// invocations and breaking screenshot/diff-based comparisons.
	//
	// LEAD KEYS FIRST, because a top-level table cell is capped at
	// maxTextCellWidth runes (truncateCell). Under a purely alphabetised
	// order `state` sorts behind `backendName`, `message` and `reason`,
	// so an enriched status cell was cut before the state was ever
	// reached and the column named STATE carried no state word at all.
	// That inverts the operator criterion this whole change exists to
	// serve, so the value the column is named for leads and the extra
	// keys are what a narrow column elides. Determinism is preserved:
	// the lead sequence is fixed and the remainder stays sorted.
	//
	// The lead applies ONLY to an object the pre-change build would have
	// SUMMARISED, i.e. exactly the objects the containment gate routed
	// here. That is why nestedObjectLeadKeys re-checks the replicas
	// branch's ready/available condition: a partial replicas object always
	// reached this branch and keeps its plain alphabetical order, as does
	// anything carrying no trigger key at all.
	parts := make([]string, 0, len(obj))
	seen := make(map[string]bool, len(obj))
	for _, k := range nestedObjectLeadKeys(obj) {
		if _, ok := obj[k]; ok {
			seen[k] = true
			parts = append(parts, fmt.Sprintf("%s=%s", k, formatValueWithIndent(obj[k], indent)))
		}
	}
	rest := make([]string, 0, len(obj))
	for k := range obj {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatValueWithIndent(obj[k], indent)))
	}
	return strings.Join(parts, ", ")
}

// nestedObjectLeadKeys returns the keys that should render first for obj:
// the keys consumed by the summary pattern whose trigger key obj carries,
// in the order that summary puts them. Returns nil for an object that
// triggers no pattern, so objects that always reached the default branch
// keep the plain alphabetical order they have always had.
func nestedObjectLeadKeys(obj map[string]any) []string {
	if _, ok := obj["link"]; ok {
		return []string{"name", "link"}
	}
	if _, ok := obj["state"]; ok {
		return []string{"state", "message"}
	}
	// The replicas summary needs all three keys, so a PARTIAL replicas
	// object never took that branch and has always rendered alphabetically.
	// Gate the lead the same way, or this widens the set beyond the objects
	// the containment gate routed here and moves a rendering that was never
	// in scope.
	if _, ok := obj["desired"]; ok {
		_, hasReady := obj["ready"]
		_, hasAvailable := obj["available"]
		if hasReady && hasAvailable {
			return []string{"desired", "ready", "available"}
		}
	}
	return nil
}
