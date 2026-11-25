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

	// Finding 1591: the bearer's own pre-dot segment is not a leak. A RunOS typed bearer
	// deliberately carries its public id in the clear there, and conductor's reported id for THIS
	// exact request is exactly that segment, so it must be named, not refused.
	t.Run("named: the id is the sent bearer's own pre-dot segment", func(t *testing.T) {
		credType, credID, state := verifiedCredentialReference(
			json.RawMessage(`{"credentialType":"personal_pat","credentialId":"abc123"}`), callerCredentialValue)
		if state != credentialReferenceNamed || credType != "personal_pat" || credID != "abc123" {
			t.Fatalf("got credType=%q credID=%q state=%v, want named personal_pat/abc123", credType, credID, state)
		}
	})
}

// Finding 1591, closure test: a typed bearer's real shape is
// runos_(pat|key)_<uuid>.<secret>, and the id conductor reports for THIS caller is exactly that
// uuid, in the clear. verifiedCredentialReference must accept it for both typed credential
// families, never refuse it as a leak.
func TestVerifiedCredentialReferenceAcceptsThePublicIdEmbeddedInTheBearer(t *testing.T) {
	// Not a real uuid (non-hex letters): a leakcheck-safe stand-in for the published
	// runos_pat_<uuid>.<secret> shape, shaped like a public id, never like a secret.
	const publicID = "gggggggg-gggg-gggg-gggg-gggggggggggg"
	const typedCredentialValue = "runos_pat_" + publicID + ".nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"

	cases := []struct {
		name           string
		credentialType string
	}{
		{"personal access token", "personal_pat"},
		{"account api key", "account_api_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"credentialType":"` + tc.credentialType + `","credentialId":"` + publicID + `"}`)
			credType, credID, state := verifiedCredentialReference(raw, typedCredentialValue)
			if state != credentialReferenceNamed || credType != tc.credentialType || credID != publicID {
				t.Fatalf("got credType=%q credID=%q state=%v, want named %s/%s", credType, credID, state, tc.credentialType, publicID)
			}
		})
	}
}

// The secret half (after the bearer's final ".") must still be refused however it is wrapped,
// proving the narrowed guard still protects the one part of the bearer that IS a secret.
func TestVerifiedCredentialReferenceStillRefusesTheSecretHalf(t *testing.T) {
	const publicID = "gggggggg-gggg-gggg-gggg-gggggggggggg"
	const theSecretPart = "nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"
	const typedCredentialValue = "runos_pat_" + publicID + "." + theSecretPart

	cases := []struct {
		name string
		id   string
	}{
		{"id equal to the secret half", theSecretPart},
		{"id wraps the secret half", "wrapped-" + theSecretPart + "-extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"credentialType":"personal_pat","credentialId":"` + tc.id + `"}`)
			credType, credID, state := verifiedCredentialReference(raw, typedCredentialValue)
			if state != credentialReferenceRefused || credType != "" || credID != "" {
				t.Fatalf("got credType=%q credID=%q state=%v, want refused", credType, credID, state)
			}
		})
	}
}

func repeatChar(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
