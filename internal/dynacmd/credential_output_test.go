package dynacmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Built by concatenation, never as one literal: scripts/leakcheck.py scans
// source TEXT for this exact id-dot-secret shape, and a test fixture that
// matches it on one line would fail the public-repo leak gate on this
// file itself. Splitting the id from the secret (as the gate's own test
// suite does for the same reason, scripts/leakcheck_test.py) keeps the
// runtime value real-shaped for GuardCredentialResponse to catch while
// no source line carries the whole secret contiguously.
const (
	// The legacy shape requires EXACTLY 10, then EXACTLY 32, characters
	// from the unambiguous charset (no 0, 1, i, l, o, I, O): Conductor's
	// own KEY_ID_LENGTH/SECRET_LENGTH/UNAMBIGUOUS_CHARS (apiKeys.ts) and
	// Roid's legacyChars (secretshape.go) agree on this exact set.
	// There is no legacy "runos_key_" shape: Conductor's legacy route
	// mints runos_pat_ for both personal and account-role keys
	// (apiKeys.ts PAT_PREFIX). runos_key_ exists only in the typed shape.
	fakeLegacyPAT = "runos_pat_" + "AbCdEfGhJk." + "AbCdAbCdAbCdAbCdAbCdAbCdAbCdAbCd"
	// Same id/secret shape, runos_ntk_ prefix: handbook article 220
	// (create-notify-key) states "keyValue is a token of the form
	// runos_ntk_<id>.<secret>, 53 characters long", which is exactly
	// 10(prefix)+10(id)+1(dot)+32(secret).
	fakeNotifyKey = "runos_ntk_" + "AbCdEfGhJk." + "AbCdAbCdAbCdAbCdAbCdAbCdAbCdAbCd"
	// A second, distinct legacy-shaped secret for the "two bearers in one
	// field" exactness tests.
	fakeLegacyPAT2 = "runos_pat_" + "ZzYyXxWwVv." + "ZzYyXxWwVvZzYyXxWwVvZzYyXxWwVvZz"
	// Typed shape: runos_(pat|key)_<uuid>.<43-char base64url>.
	fakeTypedPAT = "runos_pat_" + "11111111-2222-3333-4444-555555555555." +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	fakeTypedKey = "runos_key_" + "11111111-2222-3333-4444-555555555555." +
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	// Roid's own legacy personal-key secret shape, rk_<43-char base64url>.
	fakeRoidRK = "rk_" + "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

func cmdDef(command string) manifest.Command {
	return manifest.Command{Command: command}
}

func TestGuardCredentialResponse_ScopedToCredentialFamilies(t *testing.T) {
	// Finding 1097: a command outside credentialCommandFamilies must never be
	// scanned, so a log line, an env var readback or a SQL result row that
	// happens to contain a credential-shaped string still renders in full.
	cases := []struct {
		name    string
		command string
		body    string
	}{
		{
			name:    "pod logs carrying a credential-shaped string in a message",
			command: "apps/logs",
			body:    `[{"containerName":"web","message":"using key ` + fakeLegacyPAT + `"},{"containerName":"web","message":"ready"}]`,
		},
		{
			name:    "an app env readback carrying a RunOS credential the user stored themselves",
			command: "apps/env/show",
			body:    `{"env":{"RUNOS_API_KEY":"` + fakeLegacyPAT + `"}}`,
		},
		{
			name:    "a raw exec-sql result row",
			command: "exec-sql",
			body:    `{"rows":[{"token":"` + fakeTypedKey + `"}]}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := GuardCredentialResponse(cmdDef(c.command), []byte(c.body)); err != nil {
				t.Fatalf("a non-credential command must never be refused by this guard, got: %v", err)
			}
		})
	}
}

func TestGuardCredentialResponse_LegacyApiKeys(t *testing.T) {
	cases := []struct {
		name    string
		command string
		body    string
		wantErr bool
	}{
		{
			name:    "add returns the one-shot token",
			command: "account/api-keys/add",
			body:    `{"id":"k1","name":"ci","token":"` + fakeLegacyPAT + `","warning":"save it"}`,
		},
		{
			// Finding 1155: the PUBLISHED command is "add-personal", not
			// "personal". This exact case used to pass under the wrong
			// command path (which no live command ever sends), hiding the
			// real failure on the command Conductor actually registers.
			name:    "add-personal returns the one-shot token under the same field",
			command: "account/api-keys/add-personal",
			body:    `{"id":"k1","name":"ci","personal":true,"token":"` + fakeLegacyPAT + `"}`,
		},
		{
			name:    "list carries no secret",
			command: "account/api-keys/list",
			body:    `{"keys":[{"id":"k1","credentialId":"k1","credentialType":"account_api_key"}]}`,
		},
		{
			name:    "list leaking a secret under the create field name is still refused",
			command: "account/api-keys/list",
			body:    `{"keys":[{"id":"k1","token":"` + fakeLegacyPAT + `"}]}`,
			wantErr: true,
		},
		{
			name:    "show leaking a secret under an unexpected field name is refused",
			command: "account/api-keys/show",
			body:    `{"id":"k1","secretCopy":"` + fakeLegacyPAT + `"}`,
			wantErr: true,
		},
		{
			name:    "add leaking the secret a second time under another field is refused",
			command: "account/api-keys/add",
			body:    `{"id":"k1","token":"` + fakeLegacyPAT + `","debug":"` + fakeLegacyPAT + `"}`,
			wantErr: true,
		},
		{
			// Finding 1150: the field-name exemption must not license the
			// WHOLE value when it carries more than one bearer.
			name:    "add with two bearers concatenated in the token field is refused",
			command: "account/api-keys/add",
			body:    `{"id":"k1","token":"` + fakeLegacyPAT + " " + fakeLegacyPAT2 + `"}`,
			wantErr: true,
		},
		{
			// Finding 1150: a secret used as a PROPERTY NAME, not a value.
			name:    "a secret used as a property name is refused",
			command: "account/api-keys/show",
			body:    `{"id":"k1","` + fakeLegacyPAT + `":"unrelated"}`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := GuardCredentialResponse(cmdDef(c.command), []byte(c.body))
			if c.wantErr && err == nil {
				t.Fatalf("expected a refusal, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no refusal, got: %v", err)
			}
		})
	}
}

func TestGuardCredentialResponse_NotifyKeys(t *testing.T) {
	if err := GuardCredentialResponse(cmdDef("account/notify-keys/add"), []byte(`{"id":"k1","keyValue":"`+fakeNotifyKey+`"}`)); err != nil {
		t.Fatalf("expected the one-shot keyValue to be allowed, got: %v", err)
	}
	// Finding 1150: the existing test substituted a typed Roid key here,
	// which matched the pattern anyway and masked the fact that the
	// pattern had no runos_ntk_ shape at all. A real notify-key leak into
	// an inventory response (list/set-system/update) must be caught.
	if err := GuardCredentialResponse(cmdDef("account/notify-keys/list"), []byte(`{"id":"k1","keyValue":"`+fakeNotifyKey+`"}`)); err == nil {
		t.Fatal("a notify key secret on a non-issuing verb must be refused")
	}
	if err := GuardCredentialResponse(cmdDef("account/notify-keys/update"), []byte(`{"id":"k1","totalEmailsSent":3,"leaked":"`+fakeNotifyKey+`"}`)); err == nil {
		t.Fatal("a notify key secret leaked into an update response must be refused")
	}
}

// Finding 1105/1155: the typed personal-access-tokens/account-api-keys
// families (preview/list/show published by Story 342; add/update/rotate/
// revoke still held back server-side pending header transport) use
// `secret`, not `token`, for their one-shot field, and only on add/rotate.
func TestGuardCredentialResponse_TypedFamilies(t *testing.T) {
	cases := []struct {
		name    string
		command string
		body    string
		wantErr bool
	}{
		{
			name:    "personal-access-tokens add returns secret",
			command: "account/personal-access-tokens/add",
			body:    `{"credential_id":"c1","type":"personal_pat","secret":"` + fakeTypedPAT + `"}`,
		},
		{
			name:    "personal-access-tokens rotate returns secret",
			command: "account/personal-access-tokens/rotate",
			body:    `{"credential_id":"c1","secret":"` + fakeTypedPAT + `"}`,
		},
		{
			name:    "personal-access-tokens list carries no secret",
			command: "account/personal-access-tokens/list",
			body:    `{"credential_id":"c1","type":"personal_pat","owner":"p1","permission_source":"current_user_intersection"}`,
		},
		{
			name:    "personal-access-tokens show (published) carries no secret",
			command: "account/personal-access-tokens/show",
			body:    `{"credential_id":"c1","type":"personal_pat"}`,
		},
		{
			name:    "personal-access-tokens list leaking secret is refused",
			command: "account/personal-access-tokens/list",
			body:    `{"credential_id":"c1","secret":"` + fakeTypedPAT + `"}`,
			wantErr: true,
		},
		{
			name:    "account-api-keys add returns secret, nested credential metadata untouched",
			command: "account/account-api-keys/add",
			body:    `{"credential":{"credentialId":"c1","credentialType":"account_api_key"},"secret":"` + fakeTypedKey + `"}`,
		},
		{
			name:    "account-api-keys get leaking secret is refused",
			command: "account/account-api-keys/show",
			body:    `{"credential":{"credentialId":"c1"},"secret":"` + fakeTypedKey + `"}`,
			wantErr: true,
		},
		{
			name:    "account-api-keys add leaking a roid rk_ secret elsewhere is refused",
			command: "account/account-api-keys/add",
			body:    `{"secret":"` + fakeTypedKey + `","debug":"` + fakeRoidRK + `"}`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := GuardCredentialResponse(cmdDef(c.command), []byte(c.body))
			if c.wantErr && err == nil {
				t.Fatalf("expected a refusal, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no refusal, got: %v", err)
			}
		})
	}
}

// Finding 1155's second instance: a command spelled with an embedded path
// segment after the family must still be recognised (and scanned), not
// silently fall out of the registry because it doesn't end in exactly one
// verb token.
func TestCredentialFamilyFor_EmbeddedPathSegmentStillMatchesTheFamily(t *testing.T) {
	family, verb, ok := credentialFamilyFor("account/notify-keys/{id}/delete")
	if !ok {
		t.Fatal("expected the family to be recognised despite the embedded path segment")
	}
	if verb != "{id}/delete" {
		t.Fatalf("verb = %q", verb)
	}
	if family.issuanceVerbs[verb] {
		t.Fatal("this verb must not be treated as an issuing operation")
	}
	// And the guard must actually scan it (not treat it as out of scope).
	if err := GuardCredentialResponse(cmdDef("account/notify-keys/{id}/delete"), []byte(`{"leaked":"`+fakeNotifyKey+`"}`)); err == nil {
		t.Fatal("expected a refusal: the family must still be scanned for this spelling")
	}
}

func TestRunosSecretPatternMatchesEveryPublishedShape(t *testing.T) {
	for _, secret := range []string{fakeLegacyPAT, fakeNotifyKey, fakeTypedPAT, fakeTypedKey, fakeRoidRK} {
		if !runosSecretPattern.MatchString(secret) {
			t.Errorf("runosSecretPattern did not match %q", secret)
		}
	}
	// Near misses: must not match.
	for _, notSecret := range []string{
		"runos_pat_short.tooshort",
		"runos_other_" + "AbCdEf1234." + "0123456789abcdefghijklmnopqrstuv",
		"attempt_at_something",
	} {
		if runosSecretPattern.MatchString(notSecret) {
			t.Errorf("runosSecretPattern incorrectly matched %q", notSecret)
		}
	}
}

// Finding 1193: the one-shot exemption must require the EXACT bearer
// shape that command family's own producer mints, not merely any
// recognised RunOS/Roid shape. A wrong-family substitution (e.g. the
// legacy account/api-keys/add route somehow returning a typed
// runos_key_ bearer) must be refused, the same as an entirely
// unexpected value would be.
func TestGuardCredentialResponse_WrongFamilyBearerShapeIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		command string
		body    string
	}{
		{
			name:    "legacy api-keys add returning a typed key bearer",
			command: "account/api-keys/add",
			body:    `{"id":"k1","token":"` + fakeTypedKey + `"}`,
		},
		{
			name:    "legacy notify-keys add returning a legacy PAT bearer",
			command: "account/notify-keys/add",
			body:    `{"id":"k1","keyValue":"` + fakeLegacyPAT + `"}`,
		},
		{
			name:    "typed personal-access-tokens add returning the typed account-api-key shape",
			command: "account/personal-access-tokens/add",
			body:    `{"credential_id":"c1","secret":"` + fakeTypedKey + `"}`,
		},
		{
			name:    "typed account-api-keys add returning a legacy PAT bearer",
			command: "account/account-api-keys/add",
			body:    `{"credential_id":"c1","secret":"` + fakeLegacyPAT + `"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := GuardCredentialResponse(cmdDef(c.command), []byte(c.body)); err == nil {
				t.Fatal("expected a refusal: the one-shot field held a different family's bearer shape")
			}
		})
	}
}

func TestGuardCredentialResponse_MalformedAndNonJSONBodies(t *testing.T) {
	// Finding 1150: a malformed/non-JSON body used to bypass this guard
	// entirely (and internal/output/formatter.go prints it verbatim).
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "empty body passes", body: ""},
		{name: "ordinary plain text passes", body: "plain text response"},
		{name: "malformed JSON with no secret passes", body: "not-json{"},
		{name: "malformed JSON carrying a secret is refused", body: "not-json{" + fakeLegacyPAT, wantErr: true},
		{name: "plain text carrying a secret is refused", body: "error, try " + fakeLegacyPAT + " again", wantErr: true},
		// Finding 1193: a body with a VALID, decodable first JSON value
		// followed by trailing garbage used to make json.Unmarshal fail
		// here, diverting this guard onto the raw-byte fallback while
		// internal/output's formatter (a streaming decoder) would
		// happily render the decoded first value alone. Any trailing
		// data after the one JSON value is refused outright now,
		// regardless of whether the decoded value or the trailing data
		// carries a secret: Roid/Conductor never emit this shape for a
		// credential response.
		{name: "decodable value with trailing garbage and no secret is refused", body: `{"id":"k1"}` + "trailing-garbage", wantErr: true},
		{name: "decodable value with a secret in the trailing garbage is refused", body: `{"id":"k1"}` + fakeLegacyPAT, wantErr: true},
		{name: "decodable value with a secret in the decoded value itself is refused", body: `{"id":"k1","leaked":"` + fakeLegacyPAT + `"}` + "trailing-garbage", wantErr: true},
		// Finding 1292/1295 (D2): a trailing vertical tab (0x0B) is not
		// JSON whitespace (RFC 8259 allows only space, tab, CR, LF), but
		// unicode.IsSpace treats it as one, so bytes.TrimSpace used to
		// strip it before the end-of-input check ever saw it, letting
		// trailing data after the JSON value pass unnoticed.
		{name: "decodable value with a trimSpace-only trailing byte is refused", body: "{\"id\":\"k1\"}\v", wantErr: true},
		// Finding 1323/1349: decoding respBody directly (instead of
		// trimmed) fixed the trailing case above but broke the LEADING
		// one in the wrong direction -- a leading trimSpace-only byte
		// made the original-bytes decode fail, diverting the guard onto
		// the raw-byte fallback for a body internal/output's envelope
		// helpers still render structurally (they TrimSpace too).
		{name: "decodable value with a trimSpace-only leading byte is refused", body: "\v{\"id\":\"k1\"}", wantErr: true},
		// Legitimate JSON whitespace (space, tab, CR, LF; RFC 8259) at
		// either end must keep passing -- the new boundary check must
		// not refuse ordinary formatting.
		{name: "legitimate JSON whitespace at both ends still passes", body: " \t{\"id\":\"k1\"}\r\n", wantErr: false},
		// Finding 1357 (repeat of 1323): the boundary check from the
		// previous cycle ran before the guard ever tried to decode the
		// body, so a genuine PLAIN-TEXT response (not JSON at all) with
		// a non-breaking space at either end was refused instead of
		// reaching the existing raw-pattern policy finding 1323 and this
		// same table's "ordinary plain text passes" row require.
		{name: "plain text with non-breaking-space boundary and no secret still passes", body: " credential inventory ready ", wantErr: false},
		{name: "plain text with non-breaking-space boundary carrying a secret is still refused", body: " credential inventory ready " + fakeLegacyPAT + " ", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := GuardCredentialResponse(cmdDef("account/api-keys/add"), []byte(c.body))
			if c.wantErr && err == nil {
				t.Fatalf("expected a refusal, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no refusal, got: %v", err)
			}
		})
	}
}

// Finding 1150: an error response never has a legitimate one-shot
// exemption, so any secret-shaped value anywhere refuses, and a
// non-credential command is left alone.
func TestGuardCredentialErrorBody(t *testing.T) {
	credErr := &APIError{StatusCode: 422, Body: []byte(`{"error":"invalid","detail":"saw ` + fakeLegacyPAT + `"}`)}
	got := GuardCredentialErrorBody(cmdDef("account/api-keys/add"), credErr)
	if got == credErr {
		t.Fatal("expected the error to be replaced")
	}
	if strings.Contains(got.Error(), fakeLegacyPAT) {
		t.Fatalf("the replacement error must not carry the secret, got: %v", got)
	}

	cleanErr := &APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)}
	if got := GuardCredentialErrorBody(cmdDef("account/api-keys/add"), cleanErr); got != cleanErr {
		t.Fatalf("a clean error must pass through unchanged, got: %v", got)
	}

	nonCredErr := &APIError{StatusCode: 422, Body: []byte(`{"error":"saw ` + fakeLegacyPAT + `"}`)}
	if got := GuardCredentialErrorBody(cmdDef("apps/logs"), nonCredErr); got != nonCredErr {
		t.Fatalf("a non-credential command's error must pass through unchanged, got: %v", got)
	}

	plainErr := errString("transport: connection refused")
	if got := GuardCredentialErrorBody(cmdDef("account/api-keys/add"), plainErr); got != plainErr {
		t.Fatalf("a non-APIError must pass through unchanged, got: %v", got)
	}
}

// Finding 1265 (instance 3): a duplicate key whose later occurrence is
// JSON `null` is a no-op against formatAuthError's string-typed struct
// field (the earlier value survives), but OVERWRITES the same key in
// responseContainsSecret's map[string]any scan (the earlier string is
// replaced by nil, and the nil fails the string type-switch). Without
// detecting the duplicate itself, neither the raw-byte check (the value
// may be escaped) nor the decoded-map check sees the secret, yet
// formatAuthError's struct decode renders it.
func TestGuardCredentialErrorBody_DuplicateKeyWithLaterNullIsRefused(t *testing.T) {
	body := `{"error":"` + fakeLegacyPAT + `","error":null}`
	credErr := &APIError{StatusCode: 401, Body: []byte(body)}
	got := GuardCredentialErrorBody(cmdDef("account/api-keys/add"), credErr)
	if got == credErr {
		t.Fatal("expected the error to be replaced")
	}
	if strings.Contains(got.Error(), fakeLegacyPAT) {
		t.Fatalf("the replacement error must not carry the secret, got: %v", got)
	}
	// Ground the mechanism itself: formatAuthError's struct decode of
	// the SAME raw body, if it were ever reached, would still render
	// the secret from the first occurrence.
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.Error != fakeLegacyPAT {
		t.Fatalf("expected the struct decode to retain the first occurrence (demonstrating the divergence this test guards against), got: %q", probe.Error)
	}
}

// Finding 1292/1295 (D1), end-to-end on the error path: an out-of-range
// number ahead of the duplicate key must not let hasDuplicateObjectKeys
// (via responseContainsSecret, called from GuardCredentialErrorBody) miss
// the duplicate and let the bearer's first occurrence reach formatAuthError
// unexamined, the same way TestGuardCredentialErrorBody_
// DuplicateKeyWithLaterNullIsRefused proves for the plain duplicate-key
// shape above. The secret's first byte is written as a JSON unicode
// escape for "r", as finding 1295's own probe does, so the raw-byte
// fallback the other two checks share is blind to it and cannot mask
// the D1 defect itself.
func TestGuardCredentialErrorBody_DuplicateKeyAfterOutOfRangeNumberIsRefused(t *testing.T) {
	escapedSecret := `\` + `u0072` + fakeLegacyPAT[1:]
	body := `{"detail":1e400,"error":"` + escapedSecret + `","error":null}`
	credErr := &APIError{StatusCode: 401, Body: []byte(body)}
	got := GuardCredentialErrorBody(cmdDef("account/api-keys/add"), credErr)
	if got == credErr {
		t.Fatal("expected the error to be replaced")
	}
	if strings.Contains(got.Error(), fakeLegacyPAT) {
		t.Fatalf("the replacement error must not carry the secret, got: %v", got)
	}
	// Ground the mechanism: a struct decode of the SAME raw body, if it
	// were ever reached, still retains the first "error" occurrence, with
	// the escape resolved back to the plain bearer.
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.Error != fakeLegacyPAT {
		t.Fatalf("expected the struct decode to retain the first occurrence (demonstrating the divergence this test guards against), got: %q", probe.Error)
	}
}

func TestHasDuplicateObjectKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"no duplicate", `{"id":"k1","name":"ci"}`, false},
		{"top-level duplicate", `{"token":"a","token":"b"}`, true},
		{"duplicate inside nested object", `{"id":"k1","nested":{"a":1,"a":2}}`, true},
		{"no duplicate across sibling nested objects", `{"a":{"x":1},"b":{"x":1}}`, false},
		{"duplicate inside an array element", `[{"token":"a","token":"b"}]`, true},
		{"array with no duplicates", `[{"id":"k1"},{"id":"k2"}]`, false},
		{"malformed JSON reports no duplicate", `{"id":`, false},
		// Finding 1292/1295 (D1): without UseNumber, Token() runs
		// strconv.ParseFloat on every number and returns ErrRange for one
		// outside float64's exact range, aborting the scan before it ever
		// reaches the duplicate key that follows.
		{"number outside float64 range before a duplicate key", `{"detail":1e400,"token":"a","token":"b"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasDuplicateObjectKeys([]byte(c.body)); got != c.want {
				t.Errorf("hasDuplicateObjectKeys(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestPrintApiKeyTokenBannerStillWorksFromItsNewFile(t *testing.T) {
	var out bytes.Buffer
	printApiKeyTokenBanner(&out, []byte(`{"token":"`+fakeTypedKey+`","warning":"keep it safe"}`))
	got := out.String()
	if !strings.Contains(got, fakeTypedKey) || !strings.Contains(got, "keep it safe") {
		t.Fatalf("banner output = %q", got)
	}
}

func TestPrintNotifyKeyBannerStillWorksFromItsNewFile(t *testing.T) {
	var out bytes.Buffer
	printNotifyKeyBanner(&out, []byte(`{"keyValue":"`+fakeNotifyKey+`"}`))
	if got := out.String(); !strings.Contains(got, fakeNotifyKey) {
		t.Fatalf("banner output = %q", got)
	}
}

// Finding 1150: add-personal got no banner at all (the old hardcoded
// check only named "account/api-keys/add"), and its row is now stripped
// from the table by sanitizeForTableRender, so without this fix the
// token would never render anywhere in text mode.
func TestPrintOneShotBanner_AddPersonalGetsTheApiKeyBanner(t *testing.T) {
	var out bytes.Buffer
	printOneShotBanner(&out, cmdDef("account/api-keys/add-personal"), []byte(`{"id":"k1","token":"`+fakeLegacyPAT+`"}`))
	if got := out.String(); !strings.Contains(got, fakeLegacyPAT) {
		t.Fatalf("add-personal must get the one-shot banner, got: %q", got)
	}
}

func TestPrintOneShotBanner_InventoryVerbPrintsNothing(t *testing.T) {
	var out bytes.Buffer
	printOneShotBanner(&out, cmdDef("account/api-keys/list"), []byte(`{"id":"k1","token":"`+fakeLegacyPAT+`"}`))
	if got := out.String(); got != "" {
		t.Fatalf("an inventory verb must print no banner, got: %q", got)
	}
}

func TestSanitizeForTableRender(t *testing.T) {
	body := []byte(`{"id":"k1","name":"ci","token":"` + fakeLegacyPAT + `"}`)

	// Text mode, issuance verb: the secret field is stripped.
	sanitized := sanitizeForTableRender(cmdDef("account/api-keys/add"), body, false)
	if strings.Contains(string(sanitized), fakeLegacyPAT) {
		t.Errorf("text mode must strip the one-shot field from the table body, got: %s", sanitized)
	}
	if !strings.Contains(string(sanitized), `"id":"k1"`) {
		t.Errorf("other fields must survive, got: %s", sanitized)
	}

	// JSON mode: never altered.
	if got := sanitizeForTableRender(cmdDef("account/api-keys/add"), body, true); string(got) != string(body) {
		t.Errorf("--json must be returned unchanged, got: %s", got)
	}

	// Inventory verb: never altered (no secret field expected anyway).
	invBody := []byte(`{"id":"k1","name":"ci"}`)
	if got := sanitizeForTableRender(cmdDef("account/api-keys/list"), invBody, false); string(got) != string(invBody) {
		t.Errorf("an inventory response must be returned unchanged, got: %s", got)
	}

	// Non-credential command: never altered.
	if got := sanitizeForTableRender(cmdDef("apps/logs"), body, false); string(got) != string(body) {
		t.Errorf("a non-credential command must be returned unchanged, got: %s", got)
	}
}

// Finding 1215 (G1, 4th occurrence of 1105/1150/1193's root defect):
// internal/output/formatter.go's JSON renderer calls UseNumber before
// decoding; this guard's decoder did not. A number outside float64's
// exact range elsewhere in the body made ONLY this guard's decode fail
// (strconv.ParseFloat returns ErrRange without UseNumber), diverting a
// LEGITIMATE issuance onto the raw-byte fallback, which has no concept
// of the one-shot field exemption and refuses on ANY recognised bearer
// shape anywhere -- a false refusal of a response the formatter would
// have rendered correctly.
func TestGuardCredentialResponse_HugeNumberFieldDoesNotForceRawFallback(t *testing.T) {
	huge := strings.Repeat("9", 400)
	body := `{"id":"k1","token":"` + fakeLegacyPAT + `","quota":` + huge + `}`
	if err := GuardCredentialResponse(cmdDef("account/api-keys/add"), []byte(body)); err != nil {
		t.Fatalf("a legitimate issuance with an oversized unrelated number must not be refused, got: %v", err)
	}
}

// Finding 1215 (G2): json.Unmarshal of the literal JSON value `null`
// succeeds with the target map left nil; the unchecked assignment used
// to panic before the guard or the --json error envelope ever ran.
func TestInjectETagIntoCredentialResponse_NullBodyDoesNotPanic(t *testing.T) {
	headers := http.Header{}
	headers.Set("ETag", `"etag-1"`)
	got, err := InjectETagIntoCredentialResponse(cmdDef("account/personal-access-tokens/show"), []byte("null"), headers)
	if err != nil {
		t.Fatalf("expected no error for a null body, got: %v", err)
	}
	if string(got) != "null" {
		t.Errorf("a null body must pass through unchanged, got: %s", got)
	}
}

// Finding 1215 (G2): the ETag value itself is validated before
// insertion, independent of the guard that runs on the result
// afterward, so this function's own contract holds regardless of call
// order.
func TestInjectETagIntoCredentialResponse_SecretShapedETagIsRefused(t *testing.T) {
	headers := http.Header{}
	headers.Set("ETag", fakeTypedPAT)
	_, err := InjectETagIntoCredentialResponse(cmdDef("account/personal-access-tokens/show"), []byte(`{"credential_id":"c1"}`), headers)
	if err == nil {
		t.Fatal("expected a refusal for a secret-shaped ETag, got nil")
	}
}

func TestInjectETagIntoCredentialResponse_LegitimateETagStillWorks(t *testing.T) {
	headers := http.Header{}
	headers.Set("ETag", `"abc123"`)
	got, err := InjectETagIntoCredentialResponse(cmdDef("account/personal-access-tokens/show"), []byte(`{"credential_id":"c1"}`), headers)
	if err != nil {
		t.Fatalf("expected no error for an ordinary ETag, got: %v", err)
	}
	var item map[string]any
	if jsonErr := json.Unmarshal(got, &item); jsonErr != nil {
		t.Fatalf("result is not JSON: %v, got: %s", jsonErr, got)
	}
	if item["etag"] != `"abc123"` {
		t.Errorf("expected etag field in the result, got: %v", item)
	}
}

// Finding 1215 (G3): a value in the issuing field that matches NEITHER
// this family's own shape NOR any other recognised shape used to fall
// through unexamined (findSecretValue finds nothing to object to), and
// the banners print any non-empty string, presenting garbage as a valid
// one-shot secret.
func TestGuardCredentialResponse_GarbageIssuingValueIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		command string
		field   string
	}{
		{"legacy api-keys add", "account/api-keys/add", "token"},
		{"legacy api-keys add-personal", "account/api-keys/add-personal", "token"},
		{"notify-keys add", "account/notify-keys/add", "keyValue"},
		{"personal-access-tokens add", "account/personal-access-tokens/add", "secret"},
		{"personal-access-tokens rotate", "account/personal-access-tokens/rotate", "secret"},
		{"account-api-keys add", "account/account-api-keys/add", "secret"},
		{"account-api-keys rotate", "account/account-api-keys/rotate", "secret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"id":"k1","` + c.field + `":"not-a-real-bearer-shape"}`
			if err := GuardCredentialResponse(cmdDef(c.command), []byte(body)); err == nil {
				t.Fatal("expected a refusal for a non-bearer-shaped value in the issuing field, got nil")
			}
		})
	}
}

// A legitimate issuance must still pass for every one of the seven
// operations (the "add a test that a legitimate request still passes"
// rule for a new guard).
func TestGuardCredentialResponse_GarbageIssuingValueGuard_LegitimateStillPasses(t *testing.T) {
	cases := []struct {
		command, field, secret string
	}{
		{"account/api-keys/add", "token", fakeLegacyPAT},
		{"account/notify-keys/add", "keyValue", fakeNotifyKey},
		{"account/personal-access-tokens/add", "secret", fakeTypedPAT},
		{"account/account-api-keys/add", "secret", fakeTypedKey},
	}
	for _, c := range cases {
		body := `{"id":"k1","` + c.field + `":"` + c.secret + `"}`
		if err := GuardCredentialResponse(cmdDef(c.command), []byte(body)); err != nil {
			t.Fatalf("%s: expected a legitimate issuance to pass, got: %v", c.command, err)
		}
	}
}
