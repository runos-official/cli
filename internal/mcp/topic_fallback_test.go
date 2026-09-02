package mcp

import (
	"strings"
	"testing"
)

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

// The router names every key with guidance; the bare array repeats the index
// with the guidance removed. Measured at 73 keys, ~305 tokens, on the one
// payload every session is required to read.
func TestStripTopicKeysRemovesTheDuplicateIndex(t *testing.T) {
	payload := `{"instructions":"x\n- alpha - do a thing.\n- beta - do another.","topicKeys":["alpha","beta"],"cliUpdate":{"latest":"1.0.0"}}`
	keys := topicKeysFromBootstrap(payload)
	got := stripTopicKeys(payload, routerNamesKey(instructionsFromBootstrap(payload)), keys)
	if strings.Contains(got, "topicKeys") {
		t.Errorf("topicKeys survived: %s", got)
	}
	// Everything else has to be intact: cliUpdate drives the update notice and
	// instructions are the whole point of the call.
	for _, want := range []string{"cliUpdate", "1.0.0", "instructions", "alpha"} {
		if !strings.Contains(got, want) {
			t.Errorf("strip removed %q: %s", want, got)
		}
	}
	// The CLI still has the keys for its search fallback.
	if len(keys) != 2 {
		t.Errorf("keys lost before the strip: %v", keys)
	}
}

// If the router does not name a key, the array is the only way to discover it,
// so the payload must be left alone. Failing open here costs ~305 tokens;
// failing closed costs the agent its topic index.
func TestStripTopicKeysLeavesThePayloadWhenTheRouterIsIncomplete(t *testing.T) {
	payload := `{"instructions":"- alpha - do a thing.","topicKeys":["alpha","orphan"]}`
	got := stripTopicKeys(payload, routerNamesKey(instructionsFromBootstrap(payload)), topicKeysFromBootstrap(payload))
	if !strings.Contains(got, "topicKeys") {
		t.Error("stripped the index although the router omits a key")
	}
}

func TestStripTopicKeysLeavesMalformedPayloadsAlone(t *testing.T) {
	for _, bad := range []string{"not json", "", "{}", `{"topicKeys":[]}`} {
		if got := stripTopicKeys(bad, func(string) bool { return true }, topicKeysFromBootstrap(bad)); got != bad {
			t.Errorf("stripTopicKeys(%q) = %q, want it untouched", bad, got)
		}
	}
}

// The router uses `a / b / c` for closely related topics, and appends the
// guidance after " - ". Both have to resolve to the bare keys.
func TestRouterNamesKeyUnderstandsTheRouterFormat(t *testing.T) {
	named := routerNamesKey("- postgres / postgres-data-ops / mysql - per-service.\n- runos-labels (optional) - identify objects.")
	for _, k := range []string{"postgres", "postgres-data-ops", "mysql", "runos-labels"} {
		if !named(k) {
			t.Errorf("router line did not yield %q", k)
		}
	}
	if named("nosuchtopic") {
		t.Error("matched a key the router never names")
	}
}
