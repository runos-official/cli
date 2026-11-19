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
			name:    "personal creation returns the one-shot token under the same field",
			command: "account/api-keys/personal",
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
	if err := guardCredentialResponse(cmdDef("account/notify-keys/add"), []byte(`{"id":"k1","keyValue":"`+fakeTypedKey+`"}`)); err != nil {
		t.Fatalf("expected the one-shot keyValue to be allowed, got: %v", err)
	}
	if err := guardCredentialResponse(cmdDef("account/notify-keys/list"), []byte(`{"id":"k1","keyValue":"`+fakeTypedKey+`"}`)); err == nil {
		t.Fatal("a notify key secret on a non-issuing verb must be refused")
	}
}

// Finding 1105: the typed personal-access-tokens/account-api-keys families
// (not yet published by Story 342, but already shipped server-side) use
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

func TestRunosSecretPatternMatchesEveryPublishedShape(t *testing.T) {
	for _, secret := range []string{fakeLegacyPAT, fakeTypedPAT, fakeTypedKey, fakeRoidRK} {
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

func TestGuardCredentialResponse_NonJSONAndEmptyBodiesPass(t *testing.T) {
	for _, body := range []string{"", "plain text response", "not-json{"} {
		if err := guardCredentialResponse(cmdDef("account/api-keys/add"), []byte(body)); err != nil {
			t.Fatalf("body %q: expected no refusal, got: %v", body, err)
		}
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
	printNotifyKeyBanner(&out, []byte(`{"keyValue":"`+fakeTypedKey+`"}`))
	if got := out.String(); !strings.Contains(got, fakeTypedKey) {
		t.Fatalf("banner output = %q", got)
	}
}
