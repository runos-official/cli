package output

import "strings"

func teardownRemedy(record map[string]any) string {
	remedy := stringValue(record["remedy"])
	if stringValue(record["providerState"]) != "confirmed_destroyed" {
		return remedy
	}

	// Public remedies combine local cleanup and independent recovery sentences.
	// Confirmed destruction removes only advice that requires the old machine.
	var kept []string
	for _, line := range strings.Split(remedy, "\n") {
		var sentences []string
		for _, sentence := range strings.SplitAfter(line, ". ") {
			if !requiresLocalMachine(sentence) {
				sentences = append(sentences, sentence)
			}
		}
		if remaining := strings.TrimSpace(strings.Join(sentences, "")); remaining != "" {
			kept = append(kept, remaining)
		}
	}
	return strings.Join(kept, "\n")
}

func requiresLocalMachine(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"runos uninstall", "log in", "login", "ssh ",
		"surviving machine", "check the machine", "check the host",
		"kubernetes and cluster configuration", "retains kubernetes", "retain kubernetes",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
