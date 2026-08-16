package mcp

import "testing"

// Review 2 item 18. The key fallback matched in BOTH directions against
// every hyphen-split word, so a one- or two-letter word inside a key
// (`a-records`, `vm-storage`) matched any search term that happened to
// contain those letters. Only a word of three characters or more may
// match by being contained in the term.
func TestKeyMatchesAnyTerm_ShortWordsDoNotMatchInReverse(t *testing.T) {
	keys := []string{"a-records", "dns-zones"}

	if got := topicKeySuggestions("database", keys); got != nil {
		t.Errorf(`"database" must not match a key through its one-letter word, got %v`, got)
	}
	if got := topicKeySuggestions("dns", keys); len(got) != 1 || got[0] != "dns-zones" {
		t.Errorf(`"dns" must still match dns-zones, got %v`, got)
	}
	if got := topicKeySuggestions("records", keys); len(got) != 1 || got[0] != "a-records" {
		t.Errorf(`"records" must still match a-records, got %v`, got)
	}
}

// A single letter is not a search term. Left unguarded it matched most
// of the index and the fallback answered with noise.
func TestKeyMatchesAnyTerm_SingleLetterTermMatchesNothing(t *testing.T) {
	if got := topicKeySuggestions("a", []string{"virtual-machines", "gpu-passthrough"}); got != nil {
		t.Errorf("a one-letter term must match nothing, got %v", got)
	}
}

// The plural an agent types still finds the singular key. This is the
// behaviour the len>=3 rule would otherwise have removed for `vms`.
func TestTopicKeySuggestions_PluralFindsTheSingularKey(t *testing.T) {
	keys := []string{"vm-storage", "gpu-passthrough", "deploying-apps"}
	cases := map[string]string{
		"vms":  "vm-storage",
		"gpus": "gpu-passthrough",
	}
	for term, want := range cases {
		got := topicKeySuggestions(term, keys)
		if len(got) != 1 || got[0] != want {
			t.Errorf("topicKeySuggestions(%q) = %v, want [%s]", term, got, want)
		}
	}
}
