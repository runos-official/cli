package workspace

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// A synthetic close error carrying a given code, the way coder/websocket reports one to a reader.
func closeErr(code websocket.StatusCode) error {
	return fmt.Errorf("failed to get reader: %w", websocket.CloseError{Code: code, Reason: ""})
}

// Every code the gate can send a workspace client must become a sentence a person can act on, and
// none of them may leak the raw library phrase or the numeric code.
func TestRefusalTranslatesEveryGateCode(t *testing.T) {
	for _, code := range []websocket.StatusCode{4001, 4401, 4403, 4409, 4429, 4502, 4503, websocket.StatusMessageTooBig} {
		got := Refusal(closeErr(code))
		if got == nil {
			t.Fatalf("code %d produced no error", code)
		}
		msg := got.Error()
		if strings.Contains(msg, "failed to get reader") || strings.Contains(msg, "received close frame") {
			t.Errorf("code %d leaked the raw library phrase: %q", code, msg)
		}
		if strings.Contains(msg, fmt.Sprintf("%d", int(code))) {
			t.Errorf("code %d leaked its number into the message: %q", code, msg)
		}
	}
}

// The 4401 message must NOT resurrect the retired PSK model. A user told to "reset the key in the
// console" is sent to do something that no longer exists.
func TestRefusal4401DropsThePSKLanguage(t *testing.T) {
	msg := Refusal(closeErr(4401)).Error()
	for _, dead := range []string{"rotate", "rotates", "reset it", "refused the key"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(dead)) {
			t.Errorf("4401 message still uses retired PSK language %q: %q", dead, msg)
		}
	}
}

// A nil error and an ordinary non-close error pass through untouched: this only translates the
// gate's close codes, it does not invent failures.
func TestRefusalPassesThroughNonCloseErrors(t *testing.T) {
	if Refusal(nil) != nil {
		t.Error("nil must stay nil")
	}
	plain := fmt.Errorf("dial tcp: connection refused")
	if got := Refusal(plain); got.Error() != plain.Error() {
		t.Errorf("a non-close error must pass through unchanged, got %q", got)
	}
}
