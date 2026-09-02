package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The read server's topic gate could not be opened at all (goal 21, B1).
//
// `mcp_topics_search` matched conductor's `keywords` column only. Searching "vm", "vms",
// "virtual machine" or "gpu" returned 0 topics while topics on exactly those subjects existed
// under keys that contain the word. On the read server that is not a poor result, it is a
// lockout: every other tool is blocked until two distinct topics have been read, and a search
// that finds nothing can never satisfy that.
//
// The server-side half is conductor matching keys and titles as well. This is the client-side
// half: when a search comes back empty, match the caller's keywords against the topic keys the
// bootstrap already handed over, and answer with those instead of nothing. It needs no network
// call and works against any conductor.

// bootstrapTopicKey is the pseudo-key recorded when mcp_bootstrap
// succeeds. Bootstrap returns the instructions every session must
// follow, which is documentation, so it counts as one of the reads the
// topic gate demands.
const bootstrapTopicKey = "mcp-bootstrap"

// topicKeySuggestions returns the topic keys that match any of the
// caller's keywords, in the order the keys were given.
//
// A term matches when it appears anywhere in the key, or when a
// hyphen-split word of the key of at least minReverseMatchLength
// characters appears in the term, so the plural and singular an agent
// types both land ("gpu" finds `gpu-passthrough`, "gpus" finds it too).
// Abbreviations that share no letters with the spelled-out key go
// through termSynonyms.
func topicKeySuggestions(keywords string, keys []string) []string {
	terms := splitSearchTerms(keywords)
	if len(terms) == 0 {
		return nil
	}
	var out []string
	for _, key := range keys {
		if keyMatchesAnyTerm(key, terms) {
			out = append(out, key)
		}
	}
	return out
}

// termSynonyms expands the abbreviations an agent types into the words a
// topic key is actually spelled with. Deliberately tiny: only the pairs
// the review measured returning zero results ("vm", "vms" against topics
// keyed `virtual-machines`). A large synonym table would start matching
// topics the caller did not ask for, which is a worse failure than one
// missed match, because the agent reads what it is handed.
var termSynonyms = map[string][]string{
	"vm":  {"virtual", "machine"},
	"vms": {"virtual", "machine"},
}

// minTermLength is the shortest string treated as a search term. A single
// letter is not one: matched as a substring it selects most of the index,
// and the caller reads whatever it is handed (review 2 item 18).
const minTermLength = 2

// minReverseMatchLength is the shortest topic-key WORD allowed to match by
// being contained in the caller's term. The rule is asymmetric on purpose:
// `gpu` inside `gpus` is a plural, while `a` inside `database` is a
// coincidence, and the old symmetric match made every key holding a
// one-letter word answer almost every search.
const minReverseMatchLength = 3

// splitSearchTerms splits a keywords argument the same way conductor
// does (on commas, whitespace, or both) and adds any synonyms.
//
// A plural also contributes its singular, so `vms` still finds
// `vm-storage` by the forward match now that the reverse one requires a
// three-letter word.
func splitSearchTerms(keywords string) []string {
	var terms []string
	for _, raw := range strings.FieldsFunc(strings.ToLower(keywords), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if len(raw) < minTermLength {
			continue
		}
		terms = append(terms, raw)
		terms = append(terms, termSynonyms[raw]...)
		if singular := strings.TrimSuffix(raw, "s"); singular != raw && len(singular) >= minTermLength {
			terms = append(terms, singular)
		}
	}
	return terms
}

// keyMatchesAnyTerm reports whether a topic key matches any search term.
func keyMatchesAnyTerm(key string, terms []string) bool {
	lower := strings.ToLower(key)
	words := strings.FieldsFunc(lower, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
		for _, word := range words {
			if len(word) < minReverseMatchLength {
				continue
			}
			if strings.Contains(term, word) {
				return true
			}
		}
	}
	return false
}

// searchReturnedNothing reports whether a topics-search response found no
// topics. False for anything it cannot parse: an unrecognised shape is
// not a known-empty result, and answering a fallback over it would hide
// a real response.
func searchReturnedNothing(result string) bool {
	var resp struct {
		Topics []json.RawMessage `json:"topics"`
		Count  *int              `json:"count"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return false
	}
	if resp.Count == nil {
		return false
	}
	return *resp.Count == 0 && len(resp.Topics) == 0
}

// topicFallbackResult renders the client-side fallback in the same shape
// a real search returns, so the caller (and the gate's key extraction)
// needs no special case. The note says where the answer came from,
// because a topic listed here has a key but no summary.
func topicFallbackResult(keywords string, matches []string) string {
	topics := make([]map[string]any, 0, len(matches))
	for _, key := range matches {
		topics = append(topics, map[string]any{
			"key":   key,
			"title": key,
			"note":  "matched on the topic key by the CLI, because the server's keyword search returned nothing. Read the body with mcp_topics_show.",
		})
	}
	payload := map[string]any{
		"topics": topics,
		"count":  len(topics),
		"mode":   "key-match",
		"note":   fmt.Sprintf("The server's keyword search found nothing for %q, so these were matched against the topic index from mcp_bootstrap. Call mcp_topics_show with a key to read one.", keywords),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(encoded)
}

// topicKeysFromBootstrap pulls the topic index out of a bootstrap
// response so the search fallback has something to match against.
// Sorted for a stable order in every later answer.
func topicKeysFromBootstrap(result string) []string {
	var resp struct {
		TopicKeys []string `json:"topicKeys"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return nil
	}
	keys := append([]string(nil), resp.TopicKeys...)
	sort.Strings(keys)
	return keys
}

// stripTopicKeys removes the topicKeys array from what the MODEL sees.
//
// The bootstrap's topic router already names every key, with a line saying when
// to read each one, so the bare array was the same index a second time with the
// guidance removed: measured at 73 keys and about 305 tokens, on the one payload
// every session is required to read.
//
// The CLI still needs the array, which is why it is parsed out first: the search
// fallback matches a caller's words against the keys when a keyword search finds
// nothing (B1). So this drops the duplicate from the wire, not the capability.
//
// Fails OPEN, deliberately. Anything unparseable, or a payload whose router does
// not name every key, returns the original text untouched. A malformed strip
// would take the topic index away from the agent entirely, and a slightly larger
// bootstrap is a much smaller problem than one that cannot route.
func stripTopicKeys(result string, routerNames func(string) bool, keys []string) string {
	if len(keys) == 0 {
		return result
	}
	for _, k := range keys {
		if !routerNames(k) {
			return result
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &obj); err != nil {
		return result
	}
	if _, ok := obj["topicKeys"]; !ok {
		return result
	}
	delete(obj, "topicKeys")
	out, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return result
	}
	return string(out)
}

// routerNamesKey reports whether the bootstrap instructions name a topic key on
// one of the router's bullet lines, including the `a / b / c` shorthand it uses
// for closely related topics.
func routerNamesKey(instructions string) func(string) bool {
	named := map[string]struct{}{}
	for _, line := range strings.Split(instructions, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		head := strings.TrimPrefix(line, "- ")
		if i := strings.Index(head, " - "); i >= 0 {
			head = head[:i]
		}
		for _, part := range strings.Split(head, " / ") {
			part = strings.TrimSpace(part)
			if i := strings.Index(part, " ("); i >= 0 {
				part = part[:i]
			}
			if part != "" {
				named[part] = struct{}{}
			}
		}
	}
	return func(k string) bool { _, ok := named[k]; return ok }
}

// instructionsFromBootstrap pulls the instructions text out of a bootstrap
// response. Empty on anything unparseable, which makes routerNamesKey match
// nothing and stripTopicKeys leave the payload alone.
func instructionsFromBootstrap(result string) string {
	var resp struct {
		Instructions string `json:"instructions"`
	}
	if json.Unmarshal([]byte(result), &resp) != nil {
		return ""
	}
	return resp.Instructions
}
