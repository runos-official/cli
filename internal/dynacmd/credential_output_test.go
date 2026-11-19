package dynacmd

import (
	"bytes"
	"strings"
	"testing"
)

// Built by concatenation, never as one literal: scripts/leakcheck.py scans
// source TEXT for this exact id-dot-secret shape, and a test fixture that
// matches it on one line would fail the public-repo leak gate on this
// file itself. Splitting the id from the secret (as the gate's own test
// suite does for the same reason, scripts/leakcheck_test.py) keeps the
// runtime value real-shaped for guardCredentialResponse to catch while
// no source line carries the whole secret contiguously.
const (
	fakePATSecret = "runos_pat_" + "AbCdEf1234." + "0123456789abcdefghijklmnopqrstuv"
	fakeKeySecret = "runos_key_" + "AbCdEf1234." + "0123456789abcdefghijklmnopqrstuv"
)

func TestGuardCredentialResponse(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "a one-shot PAT issuance is allowed",
			body: `{"id":"k1","name":"ci","token":"` + fakePATSecret + `","warning":"save it"}`,
		},
		{
			name: "a one-shot account key issuance is allowed",
			body: `{"id":"k1","name":"ci","token":"` + fakeKeySecret + `"}`,
		},
		{
			name: "a one-shot notify key issuance is allowed",
			body: `{"id":"k1","keyValue":"` + fakeKeySecret + `"}`,
		},
		{
			name: "non-secret inventory metadata is allowed",
			body: `{"id":"k1","name":"ci","credentialType":"account_api_key","custodialAccountId":"acct1","expiresAt":"2026-01-01T00:00:00Z"}`,
		},
		{
			name: "a list of non-secret rows is allowed",
			body: `{"keys":[{"id":"k1","credentialId":"k1"},{"id":"k2","credentialId":"k2"}]}`,
		},
		{
			name:    "a secret inside a list item is refused even under the token field name",
			body:    `{"keys":[{"id":"k1","token":"` + fakePATSecret + `"}]}`,
			wantErr: true,
		},
		{
			name:    "a secret under an unexpected field name is refused",
			body:    `{"id":"k1","secretCopy":"` + fakeKeySecret + `"}`,
			wantErr: true,
		},
		{
			name:    "a secret nested inside an object is refused even under token",
			body:    `{"token":{"value":"` + fakePATSecret + `"}}`,
			wantErr: true,
		},
		{
			name:    "a bare array of secrets is refused",
			body:    `["` + fakePATSecret + `"]`,
			wantErr: true,
		},
		{
			name: "non-JSON body is left alone",
			body: "plain text response",
		},
		{
			name: "empty body is left alone",
			body: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardCredentialResponse([]byte(c.body))
			if c.wantErr && err == nil {
				t.Fatalf("expected a refusal, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no refusal, got: %v", err)
			}
		})
	}
}

func TestPrintApiKeyTokenBannerStillWorksFromItsNewFile(t *testing.T) {
	var out bytes.Buffer
	printApiKeyTokenBanner(&out, []byte(`{"token":"`+fakeKeySecret+`","warning":"keep it safe"}`))
	got := out.String()
	if !strings.Contains(got, fakeKeySecret) || !strings.Contains(got, "keep it safe") {
		t.Fatalf("banner output = %q", got)
	}
}

func TestPrintNotifyKeyBannerStillWorksFromItsNewFile(t *testing.T) {
	var out bytes.Buffer
	printNotifyKeyBanner(&out, []byte(`{"keyValue":"`+fakeKeySecret+`"}`))
	if got := out.String(); !strings.Contains(got, fakeKeySecret) {
		t.Fatalf("banner output = %q", got)
	}
}
