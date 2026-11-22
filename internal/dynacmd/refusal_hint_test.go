package dynacmd

import (
	"testing"

	"github.com/runos-official/cli/internal/auth"
)

// Ruling R7-6 (objective plan FPL403): Console, conductor's manifest
// help (story 423 criterion 6) and the CLI land the same bytes.
func TestConsoleSecurityKeyRefusalMatchesRulingR76(t *testing.T) {
	const ruling = "This action needs a registered security key. Add a security key on your MFA page, then try again."
	if consoleSecurityKeyRefusal != ruling {
		t.Fatalf("consoleSecurityKeyRefusal = %q, want the R7-6 ruling text %q", consoleSecurityKeyRefusal, ruling)
	}
}

func TestFormatRefusalHint(t *testing.T) {
	t.Parallel()
	t.Run("non-403 returns false", func(t *testing.T) {
		_, ok := formatRefusalHint(&APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)}, auth.CredentialEnvKey)
		if ok {
			t.Error("expected false for non-403")
		}
	})
	t.Run("non-APIError returns false", func(t *testing.T) {
		_, ok := formatRefusalHint(errString("transport: connection refused"), auth.CredentialEnvKey)
		if ok {
			t.Error("expected false for non-APIError")
		}
	})
	// T8: ruling R7-6. Conductor relays Roid's typed-credential 403 as-is,
	// so both the flat and the Roid-native nested shape must be recognised.
	t.Run("flat reason security_key_required prints the R7-6 sentence", func(t *testing.T) {
		body := []byte(`{"error":"Forbidden","code":"authz.needs","reason":"security_key_required"}`)
		msg, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: body}, auth.CredentialRoid)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if msg != consoleSecurityKeyRefusal {
			t.Errorf("msg = %q, want %q", msg, consoleSecurityKeyRefusal)
		}
	})
	t.Run("nested error.reason security_key_required prints the same sentence", func(t *testing.T) {
		body := []byte(`{"error":{"code":"authz.needs","reason":"security_key_required"}}`)
		msg, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: body}, auth.CredentialRoid)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if msg != consoleSecurityKeyRefusal {
			t.Errorf("msg = %q, want %q", msg, consoleSecurityKeyRefusal)
		}
	})
	t.Run("security_key_required applies for a secret credential too", func(t *testing.T) {
		body := []byte(`{"error":"Forbidden","code":"authz.needs","reason":"security_key_required"}`)
		_, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: body}, auth.CredentialEnvKey)
		if !ok {
			t.Fatal("expected ok=true")
		}
	})
	// T7: FCR 2720. The sentence match is pinned to conductor's exact
	// string so a wording change there fails this test.
	t.Run("human-only refusal for a secret credential adds the sign-in line", func(t *testing.T) {
		body := []byte(`{"error":"Forbidden: API keys cannot perform this action"}`)
		for _, kind := range []auth.CredentialKind{auth.CredentialEnvKey, auth.CredentialStoredKey} {
			msg, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: body}, kind)
			if !ok {
				t.Fatalf("%s: expected ok=true", kind)
			}
			if !contains(msg, humanOnlyAPIKeyMessage) {
				t.Errorf("%s: expected conductor's own sentence to survive, got:\n%s", kind, msg)
			}
			if !contains(msg, "human sign-in") || !contains(msg, "runos login") {
				t.Errorf("%s: expected the human-sign-in line, got:\n%s", kind, msg)
			}
		}
	})
	t.Run("human-only refusal for a non-secret credential is left alone", func(t *testing.T) {
		body := []byte(`{"error":"Forbidden: API keys cannot perform this action"}`)
		_, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: body}, auth.CredentialRoid)
		if ok {
			t.Error("a human sign-in is not refused by this message; expected false")
		}
	})
	t.Run("an unrelated 403 is left alone", func(t *testing.T) {
		_, ok := formatRefusalHint(&APIError{StatusCode: 403, Body: []byte(`{"error":"Forbidden: admin role required"}`)}, auth.CredentialEnvKey)
		if ok {
			t.Error("expected false for an unrelated 403")
		}
	})
}
