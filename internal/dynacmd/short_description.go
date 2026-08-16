package dynacmd

import "strings"

// firstSentence returns the first sentence of a manifest description, for
// use as a cobra `Short`.
//
// Short used to be the WHOLE description. RunOS manifest descriptions are
// written for agents and several run past 2000 characters, so
// `runos vms --help` rendered at roughly 25 kB: the list of verbs, which
// is the one thing that help is for, was buried. `Long` still carries
// the full text, and so does the MCP tool description. Regression
// target: goal 19 A12.
//
// A sentence ends at `.`, `!` or `?` followed by whitespace or the end of
// the string. The whitespace requirement is what keeps `3.5 GiB`,
// `vm.not_found` and `ubuntu-24.04` from being read as sentence ends,
// which matters because RunOS descriptions are full of dotted refusal
// codes and version numbers.
func firstSentence(description string) string {
	trimmed := strings.TrimSpace(description)
	for i, r := range trimmed {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		next := i + 1
		if next >= len(trimmed) {
			return trimmed
		}
		if isASCIISpace(trimmed[next]) {
			return trimmed[:next]
		}
	}
	return trimmed
}

// isASCIISpace reports whether b is a space, tab or newline. Byte-wise
// because the caller only needs to know whether the character after a
// full stop starts a new word.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
