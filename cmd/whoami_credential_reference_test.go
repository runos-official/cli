package cmd

import (
	"encoding/json"
	"testing"
)

// T3: verifiedCredentialReference refuses every unsafe or malformed
// "credential" value, printing neither field unless both pass every
// check (plan FPL583 section 2.2, all or nothing).
func TestVerifiedCredentialReferenceRefusesUnsafeValues(t *testing.T) {
	const callerCredentialValue = "runos_pat_abc123.the-secret-half"
	cases := []struct {
		name string
		raw  string
	}{
		{"id equal to the sent token", `{"credentialType":"personal_pat","credentialId":"runos_pat_abc123.the-secret-half"}`},
		{"id contains the sent token", `{"credentialType":"personal_pat","credentialId":"wrapped-runos_pat_abc123.the-secret-half-extra"}`},
		{"sent token contains the id", `{"credentialType":"personal_pat","credentialId":"abc123"}`},
		{"id in the typed runos_pat_ bearer shape", `{"credentialType":"personal_pat","credentialId":"runos_pat_other.another-secret"}`},
		{"id in the typed runos_key_ bearer shape", `{"credentialType":"account_api_key","credentialId":"runos_key_other.another-secret"}`},
		{"id in the legacy rk_ bearer shape", `{"credentialType":"legacy_roid_personal_pat","credentialId":"rk_abcdefghij"}`},
		{"id of 129 characters", `{"credentialType":"personal_pat","credentialId":"` + repeatChar('a', 129) + `"}`},
		{"id with a space", `{"credentialType":"personal_pat","credentialId":"has a space"}`},
		{"empty id", `{"credentialType":"personal_pat","credentialId":""}`},
		{"id as a number", `{"credentialType":"personal_pat","credentialId":123}`},
		{"type empty", `{"credentialType":"","credentialId":"ckb1a2b3c4d5"}`},
		{"type with an upper-case letter", `{"credentialType":"Personal_pat","credentialId":"ckb1a2b3c4d5"}`},
		{"type with a space", `{"credentialType":"personal pat","credentialId":"ckb1a2b3c4d5"}`},
		{"credential as a string", `"ckb1a2b3c4d5"`},
		{"credential as an array", `["personal_pat","ckb1a2b3c4d5"]`},
		{"an unrelated object shape", `{"foo":"bar"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credType, credID, state := verifiedCredentialReference(json.RawMessage(tc.raw), callerCredentialValue)
			if state != credentialReferenceRefused {
				t.Fatalf("state = %v, want refused", state)
			}
			if credType != "" || credID != "" {
				t.Fatalf("got credType=%q credID=%q, want both empty on refusal", credType, credID)
			}
		})
	}

	t.Run("absent: field missing", func(t *testing.T) {
		_, _, state := verifiedCredentialReference(nil, callerCredentialValue)
		if state != credentialReferenceAbsent {
			t.Fatalf("state = %v, want absent", state)
		}
	})
	t.Run("absent: field null", func(t *testing.T) {
		_, _, state := verifiedCredentialReference(json.RawMessage(`null`), callerCredentialValue)
		if state != credentialReferenceAbsent {
			t.Fatalf("state = %v, want absent", state)
		}
	})
	t.Run("named: a safe value is printed verbatim", func(t *testing.T) {
		credType, credID, state := verifiedCredentialReference(
			json.RawMessage(`{"credentialType":"personal_pat","credentialId":"ckb1a2b3c4d5"}`), callerCredentialValue)
		if state != credentialReferenceNamed || credType != "personal_pat" || credID != "ckb1a2b3c4d5" {
			t.Fatalf("got credType=%q credID=%q state=%v, want named personal_pat/ckb1a2b3c4d5", credType, credID, state)
		}
	})
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
