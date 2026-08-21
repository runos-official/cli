package vmconsole

import (
	"errors"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

/**
 * Every refusal the gate can send must become something a person can act on.
 *
 * The failure this prevents is quiet: the library's own words are "failed to get reader: received
 * close frame", which names neither what was refused nor what to do, so the next thing anyone does
 * is retry the identical command.
 */
func TestEveryGateCloseCodeIsTranslated(t *testing.T) {
	// Every code the gate defines. Kept as literals rather than imported from the gate, because the
	// numbers are a contract between two separately released programs and a shared constant would
	// hide the day one of them changes.
	for _, code := range []websocket.StatusCode{4001, 4401, 4403, 4409, 4429, 4502, 4503} {
		err := Refusal(closeErr(code))
		if err == nil {
			t.Fatalf("close %d produced no error", code)
		}
		text := err.Error()
		if strings.Contains(text, "failed to get reader") || strings.Contains(text, "close frame") {
			t.Errorf("close %d was not translated: %s", code, text)
		}
		if strings.Contains(text, "4401") || strings.Contains(text, string(rune(code))) {
			t.Errorf("close %d leaks the raw code at a person: %s", code, text)
		}
	}
}

/** Anything unrecognised passes through unchanged rather than being flattened into a guess. */
func TestAnUnknownEndingIsNotInvented(t *testing.T) {
	original := errors.New("some transport problem")
	if got := Refusal(original); got != original {
		t.Fatalf("an unrecognised error was rewritten to %q", got)
	}
}

func closeErr(code websocket.StatusCode) error {
	return websocket.CloseError{Code: code, Reason: ""}
}
