package dynacmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Built by concatenation, never as one literal: scripts/leakcheck.py scans
// source TEXT for this exact id-dot-secret shape, and a test fixture that
// matches it on one line would fail the public-repo leak gate on this
// file itself. Splitting the id from the secret (as the gate's own test
// suite does for the same reason, scripts/leakcheck_test.py) keeps the
// runtime value real-shaped for guardCredentialResponse to catch while
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
			if err := guardCredentialResponse(cmdDef(c.command), []byte(c.body)); err != nil {
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
			err := guardCredentialResponse(cmdDef(c.command), []byte(c.body))
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
	if err := guardCredentialResponse(cmdDef("account/notify-keys/add"), []byte(`{"id":"k1","keyValue":"`+fakeNotifyKey+`"}`)); err != nil {
		t.Fatalf("expected the one-shot keyValue to be allowed, got: %v", err)
	}
	// Finding 1150: the existing test substituted a typed Roid key here,
	// which matched the pattern anyway and masked the fact that the
	// pattern had no runos_ntk_ shape at all. A real notify-key leak into
	// an inventory response (list/set-system/update) must be caught.
	if err := guardCredentialResponse(cmdDef("account/notify-keys/list"), []byte(`{"id":"k1","keyValue":"`+fakeNotifyKey+`"}`)); err == nil {
		t.Fatal("a notify key secret on a non-issuing verb must be refused")
	}
	if err := guardCredentialResponse(cmdDef("account/notify-keys/update"), []byte(`{"id":"k1","totalEmailsSent":3,"leaked":"`+fakeNotifyKey+`"}`)); err == nil {
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
			err := guardCredentialResponse(cmdDef(c.command), []byte(c.body))
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
	if err := guardCredentialResponse(cmdDef("account/notify-keys/{id}/delete"), []byte(`{"leaked":"`+fakeNotifyKey+`"}`)); err == nil {
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
			if err := guardCredentialResponse(cmdDef(c.command), []byte(c.body)); err == nil {
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardCredentialResponse(cmdDef("account/api-keys/add"), []byte(c.body))
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
	got := guardCredentialErrorBody(cmdDef("account/api-keys/add"), credErr)
	if got == credErr {
		t.Fatal("expected the error to be replaced")
	}
	if strings.Contains(got.Error(), fakeLegacyPAT) {
		t.Fatalf("the replacement error must not carry the secret, got: %v", got)
	}

	cleanErr := &APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)}
	if got := guardCredentialErrorBody(cmdDef("account/api-keys/add"), cleanErr); got != cleanErr {
		t.Fatalf("a clean error must pass through unchanged, got: %v", got)
	}

	nonCredErr := &APIError{StatusCode: 422, Body: []byte(`{"error":"saw ` + fakeLegacyPAT + `"}`)}
	if got := guardCredentialErrorBody(cmdDef("apps/logs"), nonCredErr); got != nonCredErr {
		t.Fatalf("a non-credential command's error must pass through unchanged, got: %v", got)
	}

	plainErr := errString("transport: connection refused")
	if got := guardCredentialErrorBody(cmdDef("account/api-keys/add"), plainErr); got != plainErr {
		t.Fatalf("a non-APIError must pass through unchanged, got: %v", got)
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
