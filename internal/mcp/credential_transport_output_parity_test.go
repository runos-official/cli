package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Story 347 criterion 5: the one test that proves M1 (header-located
// fields never reach the body), M2 (they reach the request as real HTTP
// headers), M4 (the credential output guard and ETag read-back) and M5
// (the credential error-body guard) are all applied on the MCP surface,
// the same way the cobra path (internal/dynacmd) already applies them.
//
// Fixtures are copied from Conductor's own field constants, not
// invented: src/util/cliManifest/credentialHeaderFields.ts (the shared
// IDEMPOTENCY_KEY_FIELD/IF_MATCH_FIELD, location:"header"),
// personalAccessTokens.manifest.ts, accountApiKeys.manifest.ts and
// apiKeys.manifest.ts, all read with repo_file at commit 4a7af046
// (Conductor's HEAD at the time this test was written). The eight typed
// lifecycle commands (add/update/rotate/revoke for both families) are
// held out of the live manifest by Conductor's own
// HEADER_TRANSPORT_SUPPORTED=false gate; this test builds them as Go
// fixtures the same way personalAccessTokensCommandsPendingHeaderTransport
// does on the Conductor side, exactly as the plan's "Build one manifest
// fixture" instruction asks.

const (
	ptpFakeLegacyPAT = "runos_pat_" + "AbCdEfGhJk." + "AbCdAbCdAbCdAbCdAbCdAbCdAbCdAbCd"
	ptpFakeTypedPAT  = "runos_pat_" + "11111111-2222-3333-4444-555555555555." +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	ptpFakeTypedKey = "runos_key_" + "11111111-2222-3333-4444-555555555555." +
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

// idempotencyKeyField / ifMatchField mirror Conductor's
// credentialHeaderFields.ts IDEMPOTENCY_KEY_FIELD/IF_MATCH_FIELD: name,
// type and location copied verbatim (description omitted, immaterial
// to transport).
var (
	idempotencyKeyField = manifest.Field{Name: "idempotencyKey", Type: "string", Required: true, Location: "header"}
	ifMatchField        = manifest.Field{Name: "ifMatch", Type: "string", Required: true, Location: "header"}
)

// credentialParityCase is one row of the table: one manifest command,
// its MCP tool name, the args a caller would send, which of those args
// are header-located (name -> canonical header name), whether the verb
// is an issuing operation (and under which output field), and the
// secret shape that family's own producer mints (for the one-shot and
// leak checks).
type credentialParityCase struct {
	name          string
	cmdDef        manifest.Command
	toolName      string
	args          map[string]any
	headerFields  map[string]string // arg name -> canonical HTTP header name
	isIssuance    bool
	secretField   string
	secretShape   string
	leakableField string // an ordinary output field this family's guard must still scan
}

var credentialParityCases = []credentialParityCase{
	{
		name:     "account/personal-access-tokens/add",
		toolName: "account_personal-access-tokens_add",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/add", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				{Name: "expiresAt", Type: "string", Required: true},
				idempotencyKeyField,
			}},
		},
		args: map[string]any{
			"name": "ci", "expiresAt": "2027-01-01T00:00:00Z",
			"idempotencyKey": "11111111-2222-3333-4444-555555555555",
		},
		headerFields:  map[string]string{"idempotencyKey": "Idempotency-Key"},
		isIssuance:    true,
		secretField:   "secret",
		secretShape:   ptpFakeTypedPAT,
		leakableField: "name",
	},
	{
		name:     "account/personal-access-tokens/update",
		toolName: "account_personal-access-tokens_update",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/update", Method: http.MethodPatch,
			Endpoint: "/:aid/account/personal-access-tokens/:credential", MCP: []string{"write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				{Name: "name", Type: "string"},
				ifMatchField,
			}},
		},
		args: map[string]any{
			"credential": "pat_abc", "name": "renamed",
			"ifMatch": `"etag-precondition-value"`,
		},
		headerFields:  map[string]string{"ifMatch": "If-Match"},
		leakableField: "name",
	},
	{
		name:     "account/personal-access-tokens/rotate",
		toolName: "account_personal-access-tokens_rotate",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/rotate", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens/:credential/rotate", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				idempotencyKeyField, ifMatchField,
			}},
		},
		args: map[string]any{
			"credential":     "pat_abc",
			"idempotencyKey": "11111111-2222-3333-4444-555555555555",
			"ifMatch":        `"etag-precondition-value"`,
		},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key", "ifMatch": "If-Match"},
		isIssuance:   true, secretField: "secret", secretShape: ptpFakeTypedPAT,
		leakableField: "credential_id",
	},
	{
		name:     "account/personal-access-tokens/revoke",
		toolName: "account_personal-access-tokens_revoke",
		cmdDef: manifest.Command{
			Command: "account/personal-access-tokens/revoke", Method: http.MethodPost,
			Endpoint: "/:aid/account/personal-access-tokens/:credential/revoke", MCP: []string{"write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				ifMatchField,
			}},
		},
		args:          map[string]any{"credential": "pat_abc", "ifMatch": `"etag-precondition-value"`, "confirm": true},
		headerFields:  map[string]string{"ifMatch": "If-Match"},
		leakableField: "credential_id",
	},
	{
		name:     "account/account-api-keys/add",
		toolName: "account_account-api-keys_add",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/add", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				{Name: "sourceRanges", Type: "array", ItemType: "string"},
				idempotencyKeyField,
			}},
		},
		args: map[string]any{
			"name": "ci-key", "idempotencyKey": "22222222-3333-4444-5555-666666666666",
		},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key"},
		isIssuance:   true, secretField: "secret", secretShape: ptpFakeTypedKey,
		leakableField: "name",
	},
	{
		name:     "account/account-api-keys/update",
		toolName: "account_account-api-keys_update",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/update", Method: http.MethodPatch,
			Endpoint: "/:aid/account/account-api-keys/:credential", MCP: []string{"write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				{Name: "name", Type: "string"},
				ifMatchField,
			}},
		},
		args: map[string]any{
			"credential": "key_abc", "name": "renamed",
			"ifMatch": `"etag-precondition-value"`,
		},
		headerFields:  map[string]string{"ifMatch": "If-Match"},
		leakableField: "name",
	},
	{
		name:     "account/account-api-keys/rotate",
		toolName: "account_account-api-keys_rotate",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/rotate", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys/:credential/rotate", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				idempotencyKeyField, ifMatchField,
			}},
		},
		args: map[string]any{
			"credential":     "key_abc",
			"idempotencyKey": "22222222-3333-4444-5555-666666666666",
			"ifMatch":        `"etag-precondition-value"`,
		},
		headerFields: map[string]string{"idempotencyKey": "Idempotency-Key", "ifMatch": "If-Match"},
		isIssuance:   true, secretField: "secret", secretShape: ptpFakeTypedKey,
		leakableField: "credential_id",
	},
	{
		name:     "account/account-api-keys/revoke",
		toolName: "account_account-api-keys_revoke",
		cmdDef: manifest.Command{
			Command: "account/account-api-keys/revoke", Method: http.MethodPost,
			Endpoint: "/:aid/account/account-api-keys/:credential/revoke", MCP: []string{"write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "credential", Type: "string", Positional: true},
				ifMatchField,
			}},
		},
		args:          map[string]any{"credential": "key_abc", "ifMatch": `"etag-precondition-value"`, "confirm": true},
		headerFields:  map[string]string{"ifMatch": "If-Match"},
		leakableField: "credential_id",
	},
	{
		name:     "account/api-keys/add",
		toolName: "account_api-keys_add",
		cmdDef: manifest.Command{
			Command: "account/api-keys/add", Method: http.MethodPost,
			Endpoint: "/:aid/account/api-keys", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				{Name: "expiresAt", Type: "string", Required: true},
				{Name: "accountRole", Type: "string", Enum: []string{"admin", "limited", "viewer"}},
			}},
		},
		args:       map[string]any{"name": "ci", "expiresAt": "2027-01-01T00:00:00Z"},
		isIssuance: true, secretField: "token", secretShape: ptpFakeLegacyPAT,
		leakableField: "name",
	},
	{
		name:     "account/api-keys/add-personal",
		toolName: "account_api-keys_add-personal",
		cmdDef: manifest.Command{
			Command: "account/api-keys/add-personal", Method: http.MethodPost,
			Endpoint: "/:aid/account/api-keys/personal", MCP: []string{"sensitive_write"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "name", Type: "string", Required: true},
				{Name: "expiresAt", Type: "string", Required: true},
			}},
		},
		args:       map[string]any{"name": "ci", "expiresAt": "2027-01-01T00:00:00Z"},
		isIssuance: true, secretField: "token", secretShape: ptpFakeLegacyPAT,
		leakableField: "name",
	},
	{
		name:     "account/api-keys/list",
		toolName: "account_api-keys_list",
		cmdDef: manifest.Command{
			Command: "account/api-keys/list", Method: http.MethodGet,
			Endpoint: "/:aid/account/api-keys", MCP: []string{"read"},
			Input: &manifest.Input{Fields: []manifest.Field{}},
		},
		args:          map[string]any{},
		leakableField: "name",
	},
	{
		name:     "account/api-keys/show",
		toolName: "account_api-keys_show",
		cmdDef: manifest.Command{
			Command: "account/api-keys/show", Method: http.MethodGet,
			Endpoint: "/:aid/account/api-keys/:id", MCP: []string{"read"},
			Input: &manifest.Input{Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true},
			}},
		},
		args:          map[string]any{"id": "k1"},
		leakableField: "name",
	},
}

func newParityExecutor(t *testing.T, cmdDef manifest.Command, handler http.HandlerFunc) *CommandExecutor {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	cfg := &config.Config{ConductorURL: srv.URL, AccountID: "acct1", APIKey: "test-token"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	return NewCommandExecutor(m, srv.URL)
}

func TestMCPCredentialTransportAndOutputParity(t *testing.T) {
	for _, tc := range credentialParityCases {
		tc := tc
		t.Run(tc.name+"/transport_and_etag", func(t *testing.T) {
			var gotURL *url.URL
			var gotHeader http.Header
			var gotBody []byte
			executor := newParityExecutor(t, tc.cmdDef, func(w http.ResponseWriter, r *http.Request) {
				gotURL = r.URL
				gotHeader = r.Header.Clone()
				gotBody, _ = jsonBodyOf(r)
				w.Header().Set("ETag", "W/\"server-etag-value\"")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(normalBodyFor(tc)))
			})

			out, err := executor.Execute(tc.toolName, cloneArgs(tc.args))
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}

			rawQuery := ""
			if gotURL != nil {
				rawQuery = gotURL.RawQuery
			}
			decodedBody := map[string]any{}
			_ = json.Unmarshal(gotBody, &decodedBody)

			// (a) every header-located field appears as its canonical
			// header and in neither the decoded body nor the raw query.
			for field, canonical := range tc.headerFields {
				want, _ := tc.args[field].(string)
				if got := gotHeader.Get(canonical); got != want {
					t.Errorf("header %s = %q, want %q", canonical, got, want)
				}
				if _, present := decodedBody[field]; present {
					t.Errorf("header-located field %q also appeared in the decoded body", field)
				}
				if strings.Contains(rawQuery, field) || strings.Contains(rawQuery, want) {
					t.Errorf("header-located field %q also appeared in the raw query %q", field, rawQuery)
				}
			}

			// (b) no other field moved to a header: every ordinary
			// (non-header, non-positional) MANIFEST-DECLARED field must
			// still be visible on the wire, either in the decoded body
			// (POST/PATCH) or in the raw query (GET). "confirm" is a
			// tool-schema-only control argument (refuseUnconfirmedDestructive),
			// never a manifest field, and deliberately never reaches
			// the wire; skip anything tc.cmdDef.Input does not declare.
			for field, value := range tc.args {
				if _, isHeader := tc.headerFields[field]; isHeader {
					continue
				}
				declared, positional := false, false
				if tc.cmdDef.Input != nil {
					for _, f := range tc.cmdDef.Input.Fields {
						if f.Name == field {
							declared = true
							positional = f.Positional
						}
					}
				}
				if !declared || positional {
					continue
				}
				switch tc.cmdDef.Method {
				case http.MethodGet, http.MethodDelete:
					s, _ := value.(string)
					if s != "" && !strings.Contains(rawQuery, s) {
						t.Errorf("ordinary field %q=%v missing from the raw query %q", field, value, rawQuery)
					}
				default:
					if _, present := decodedBody[field]; !present {
						t.Errorf("ordinary field %q missing from the decoded body %s", field, gotBody)
					}
				}
			}

			// (c) a credential response's ETag reaches the output, as a
			// top-level "etag" field, for every single-object response
			// (no-op for the list command, which is an array).
			if tc.cmdDef.Method != http.MethodGet || !strings.Contains(tc.name, "/list") {
				if !strings.Contains(out, `"etag": "W/\"server-etag-value\""`) {
					t.Errorf("output did not carry the response ETag: %s", out)
				}
			}
		})

		if tc.isIssuance {
			t.Run(tc.name+"/one_shot_secret_rendered_once", func(t *testing.T) {
				executor := newParityExecutor(t, tc.cmdDef, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(issuanceBodyFor(tc, tc.secretShape)))
				})
				out, err := executor.Execute(tc.toolName, cloneArgs(tc.args))
				if err != nil {
					t.Fatalf("a legitimate single-occurrence issuance response was refused: %v", err)
				}
				if n := strings.Count(out, tc.secretShape); n != 1 {
					t.Errorf("one-shot secret appeared %d times in output, want exactly 1: %s", n, out)
				}
			})
		} else {
			t.Run(tc.name+"/inventory_leak_refused", func(t *testing.T) {
				executor := newParityExecutor(t, tc.cmdDef, func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(leakingBodyFor(tc, ptpFakeLegacyPAT)))
				})
				_, err := executor.Execute(tc.toolName, cloneArgs(tc.args))
				leaked := ptpFakeLegacyPAT
				if err == nil {
					t.Fatalf("an inventory response with a secret-shaped value in %q was not refused", tc.leakableField)
				}
				if strings.Contains(err.Error(), leaked) || strings.Contains(err.Error(), ptpFakeTypedPAT) || strings.Contains(err.Error(), ptpFakeTypedKey) {
					t.Fatalf("refusal echoed the secret: %v", err)
				}
			})
		}

		t.Run(tc.name+"/error_body_secret_refused", func(t *testing.T) {
			executor := newParityExecutor(t, tc.cmdDef, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":"validation failed","detail":"saw ` + ptpFakeLegacyPAT + `"}`))
			})
			_, err := executor.Execute(tc.toolName, cloneArgs(tc.args))
			if err == nil {
				t.Fatalf("an error body with a secret-shaped value was not refused")
			}
			if strings.Contains(err.Error(), ptpFakeLegacyPAT) {
				t.Fatalf("error refusal echoed the secret: %v", err)
			}
		})
	}
}

// Instance 3 of finding 1196 (the query-string half): already closed in
// story 343 through the shared dynacmd.QueryParams, which both surfaces
// call. Asserted here for MCP too, with a synthetic GET fixture (no
// real GET/DELETE typed-lifecycle command carries a header field today)
// so a future one is covered by the same mechanism without a code
// change.
func TestMCPQueryParamsForSkipsHeaderLocatedFields(t *testing.T) {
	cmdDef := manifest.Command{
		Command: "account/access/grants/list", Method: http.MethodGet,
		Endpoint: "/:aid/account/access/grants",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "subject", Type: "string"},
			ifMatchField,
		}},
	}
	args := map[string]any{"subject": "account_api_key:key_abc", "ifMatch": "etag-value"}
	values := queryParamsFor(cmdDef, args, cmdDef.Endpoint)
	if values.Get("subject") != "account_api_key:key_abc" {
		t.Errorf("ordinary query field dropped: %v", values)
	}
	if values.Get("ifMatch") != "" {
		t.Errorf("header-located field %q reached the query string: %v", "ifMatch", values)
	}
	dynacmdValues := dynacmd.QueryParams(cmdDef, args)
	if values.Encode() != dynacmdValues.Encode() {
		t.Errorf("MCP and dynacmd disagree: mcp=%q dynacmd=%q", values.Encode(), dynacmdValues.Encode())
	}
}

func cloneArgs(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// jsonBodyOf reads and returns the raw request body (nil for a request
// with none, e.g. GET/DELETE).
func jsonBodyOf(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

// credentialResponseObject is the common shape every fixture's success
// response shares: a public id and tc's designated leakable field, with
// no secret anywhere.
func credentialResponseObject(tc credentialParityCase) map[string]any {
	return map[string]any{
		"credential_id":  "cred_abc",
		tc.leakableField: "a-normal-value",
	}
}

// encodeCredentialResponse wraps obj in an array for the one list
// command (Conductor's list output is always an array; ETag injection
// is correctly a no-op there), an object for every other command.
func encodeCredentialResponse(tc credentialParityCase, obj map[string]any) string {
	if strings.HasSuffix(tc.name, "/list") {
		encoded, _ := json.Marshal([]any{obj})
		return string(encoded)
	}
	encoded, _ := json.Marshal(obj)
	return string(encoded)
}

// normalBodyFor is the happy-path response for the transport/ETag
// subtest: no secret anywhere, nothing for the output guard to object
// to.
func normalBodyFor(tc credentialParityCase) string {
	return encodeCredentialResponse(tc, credentialResponseObject(tc))
}

// issuanceBodyFor embeds secret under tc.secretField, exactly once,
// the one documented shape a real issuance response carries.
func issuanceBodyFor(tc credentialParityCase, secret string) string {
	obj := credentialResponseObject(tc)
	obj[tc.secretField] = secret
	return encodeCredentialResponse(tc, obj)
}

// leakingBodyFor replaces tc's designated leakable field (an ordinary,
// non-issuing field no family ever licenses to carry a secret) with
// canary, simulating a server-side leak into a ordinary response.
func leakingBodyFor(tc credentialParityCase, canary string) string {
	obj := credentialResponseObject(tc)
	obj[tc.leakableField] = canary
	return encodeCredentialResponse(tc, obj)
}
