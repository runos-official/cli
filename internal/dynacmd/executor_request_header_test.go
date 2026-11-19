package dynacmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Review finding 1151: Conductor's typed credential-lifecycle commands
// (account/personal-access-tokens, account/account-api-keys) mark
// idempotencyKey and ifMatch with `location: 'header'`
// (src/util/cliManifest/credentialHeaderFields.ts, commit 4a7af046,
// IDEMPOTENCY_KEY_FIELD / IF_MATCH_FIELD), and Conductor itself holds
// every command that needs them out of the published manifest
// (HEADER_TRANSPORT_SUPPORTED = false) until the CLI can route a
// Location:"header" field as a real HTTP header instead of a JSON body
// member or a query parameter. These tests prove the routing with
// synthetic fixtures mirroring those two field definitions exactly
// (name, required, location), against a loopback server that checks
// headers and body separately -- the "transport fixture" Conductor's
// own comment names as the unblocking proof.

func TestHeaderNameFromFieldName(t *testing.T) {
	cases := []struct {
		field, want string
	}{
		{"idempotencyKey", "Idempotency-Key"},
		{"ifMatch", "If-Match"},
		{"name", "Name"},
		{"accountRole", "Account-Role"},
	}
	for _, c := range cases {
		if got := headerNameFromFieldName(c.field); got != c.want {
			t.Errorf("headerNameFromFieldName(%q) = %q, want %q", c.field, got, c.want)
		}
	}
}

func TestSplitHeaderFields(t *testing.T) {
	cmdWithHeaders := manifest.Command{
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "name", Type: "string"},
			{Name: "idempotencyKey", Type: "string", Required: true, Location: "header"},
			{Name: "ifMatch", Type: "string", Required: true, Location: "header"},
		}},
	}
	body := map[string]any{"name": "ci", "idempotencyKey": "idem-1", "ifMatch": "etag-1"}
	remaining, headers := splitHeaderFields(body, cmdWithHeaders)
	if _, present := remaining["idempotencyKey"]; present {
		t.Error("idempotencyKey must be removed from the body")
	}
	if _, present := remaining["ifMatch"]; present {
		t.Error("ifMatch must be removed from the body")
	}
	if remaining["name"] != "ci" {
		t.Errorf("name must survive in the body, got: %v", remaining)
	}
	if headers.Get("Idempotency-Key") != "idem-1" {
		t.Errorf("Idempotency-Key header = %q", headers.Get("Idempotency-Key"))
	}
	if headers.Get("If-Match") != "etag-1" {
		t.Errorf("If-Match header = %q", headers.Get("If-Match"))
	}

	// A command with no header-located field is returned unchanged, with a nil header map.
	plainCmd := manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{{Name: "name", Type: "string"}}}}
	plainBody := map[string]any{"name": "ci"}
	remaining2, headers2 := splitHeaderFields(plainBody, plainCmd)
	if len(remaining2) != 1 || remaining2["name"] != "ci" {
		t.Errorf("body must be unchanged for a command with no header fields, got: %v", remaining2)
	}
	if len(headers2) != 0 {
		t.Errorf("expected no headers, got: %v", headers2)
	}
}

// personalAccessTokenRotateFixture mirrors Conductor's real
// account/personal-access-tokens/rotate command (personalAccessTokens.manifest.ts,
// commit 4a7af046): a positional credential id plus IDEMPOTENCY_KEY_FIELD
// and IF_MATCH_FIELD, both required and header-located. Not reachable
// through the live manifest yet (Story 342 holds it back), but its field
// shapes are exact, which is what this transport test needs.
var personalAccessTokenRotateFixture = manifest.Command{
	Command:  "account/personal-access-tokens/rotate",
	Method:   http.MethodPost,
	Endpoint: "/:aid/account/personal-access-tokens/:credential/rotate",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "credential", Type: "string", Required: true, Positional: true},
		{Name: "idempotencyKey", Type: "string", Required: true, Location: "header"},
		{Name: "ifMatch", Type: "string", Required: true, Location: "header"},
	}},
}

func TestDispatch_HeaderLocatedFieldsRouteAsRealHeadersNeverBodyOrQuery(t *testing.T) {
	var gotHeaders http.Header
	var gotBody map[string]any
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &gotBody)
		}
		w.Header().Set("ETag", `"etag-value-1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"credential_id":"c1"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "acct1"}
	input := map[string]any{"credential": "c1", "idempotencyKey": "idem-42", "ifMatch": `"etag-value-0"`}
	respBody, headers, err := NewExecutor(srv.URL).dispatch(personalAccessTokenRotateFixture, []string{"c1"}, input, "", cfg, "test-token")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if got := gotHeaders.Get("Idempotency-Key"); got != "idem-42" {
		t.Errorf("Idempotency-Key header = %q, want %q", got, "idem-42")
	}
	if got := gotHeaders.Get("If-Match"); got != `"etag-value-0"` {
		t.Errorf("If-Match header = %q, want %q", got, `"etag-value-0"`)
	}
	// NEVER in the body.
	if _, present := gotBody["idempotencyKey"]; present {
		t.Errorf("idempotencyKey must never reach the JSON body, got body: %v", gotBody)
	}
	if _, present := gotBody["ifMatch"]; present {
		t.Errorf("ifMatch must never reach the JSON body, got body: %v", gotBody)
	}
	// NEVER in the query string.
	if gotQuery != "" {
		t.Errorf("expected no query string, got: %q", gotQuery)
	}

	// The response's ETag header is captured and returned to the caller.
	if headers.Get("ETag") != `"etag-value-1"` {
		t.Errorf("response ETag header = %q", headers.Get("ETag"))
	}
	if string(respBody) != `{"credential_id":"c1"}` {
		t.Errorf("respBody = %s", respBody)
	}
}

// Finding 1151's "preserve the ETag" half: a credential-family command's
// response gets the server's ETag header injected as a body field, so a
// later update/rotate/revoke has something to read for ifMatch. Uses
// account/personal-access-tokens/show, which IS published today.
func TestExecute_CredentialFamily_ETagHeaderSurfacedInResponseBody(t *testing.T) {
	showCmd := manifest.Command{
		Command:  "account/personal-access-tokens/show",
		Method:   http.MethodGet,
		Endpoint: "/:aid/account/personal-access-tokens/:credential",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "credential", Type: "string", Required: true, Positional: true},
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"credential_id":"c1","type":"personal_pat"}`))
	}))
	t.Cleanup(srv.Close)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, showCmd, true, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	if err := exec.Execute(cmd, []string{"c1"}, showCmd); err != nil {
		read()
		t.Fatalf("Execute: %v", err)
	}
	stdout, _ := read()
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got["etag"] != `"abc123"` {
		t.Errorf("expected etag field in the response, got: %v", got)
	}
	if got["credential_id"] != "c1" {
		t.Errorf("original fields must survive, got: %v", got)
	}
}

// Finding 1193: injectETagIntoCredentialResponse used to run AFTER
// guardCredentialResponse, so a secret-shaped ETag header value reached
// stdout with nothing having scanned it. Server-controlled data (the
// ETag header) carrying a bearer-shaped value is exactly as untrusted as
// any other field in the body; Execute must refuse it the same way.
func TestExecute_CredentialFamily_SecretShapedETagIsRefused(t *testing.T) {
	showCmd := manifest.Command{
		Command:  "account/personal-access-tokens/show",
		Method:   http.MethodGet,
		Endpoint: "/:aid/account/personal-access-tokens/:credential",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "credential", Type: "string", Required: true, Positional: true},
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", fakeTypedPAT)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"credential_id":"c1","type":"personal_pat"}`))
	}))
	t.Cleanup(srv.Close)
	warnEnv(t, localhostURL(srv.URL))

	cmd := warnCmd(t, showCmd, false, false)
	exec := NewExecutor(localhostURL(srv.URL))

	read := captureSplit(t)
	err := exec.Execute(cmd, []string{"c1"}, showCmd)
	stdout, stderr := read()
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if strings.Contains(stdout, fakeTypedPAT) || strings.Contains(stderr, fakeTypedPAT) || strings.Contains(err.Error(), fakeTypedPAT) {
		t.Fatalf("the secret-shaped ETag must never reach output, stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

// Finding 1194: a header-located field on a GET/DELETE command used to
// land in the query string (built by buildEndpoint, via QueryParams,
// before splitHeaderFields ever runs) in addition to the real header,
// because QueryParams had no Location check. The rotate fixture above
// uses POST, whose body-based routing never exercises buildEndpoint's
// GET/DELETE query-string branch; this synthetic GET fixture does.
var personalAccessTokenShowWithIfMatchFixture = manifest.Command{
	Command:  "account/personal-access-tokens/show-with-if-match",
	Method:   http.MethodGet,
	Endpoint: "/:aid/account/personal-access-tokens/:credential",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "credential", Type: "string", Required: true, Positional: true},
		{Name: "ifMatch", Type: "string", Required: true, Location: "header"},
	}},
}

func TestDispatch_HeaderLocatedFieldOnGETNeverReachesQueryString(t *testing.T) {
	var gotHeaders http.Header
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"credential_id":"c1"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{AccountID: "acct1"}
	input := map[string]any{"credential": "c1", "ifMatch": `"etag-value-0"`}
	_, _, err := NewExecutor(srv.URL).dispatch(personalAccessTokenShowWithIfMatchFixture, []string{"c1"}, input, "", cfg, "test-token")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := gotHeaders.Get("If-Match"); got != `"etag-value-0"` {
		t.Errorf("If-Match header = %q, want %q", got, `"etag-value-0"`)
	}
	if gotQuery != "" {
		t.Errorf("ifMatch must never reach the query string, got: %q", gotQuery)
	}
}
