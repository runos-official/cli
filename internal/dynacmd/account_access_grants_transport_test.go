package dynacmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Finding 1194 (CLI story 344 criterion 5): account/access/grants show and
// add lost their response ETag because InjectETagIntoCredentialResponse's
// family registry excluded this family (credential_output.go), and the
// published withdraw shape needed the same header-only-ifMatch proof story
// 343 gave the typed credential families. The two fixtures below mirror
// Conductor's real accountAccessGrants.manifest.ts (commit fd3a3631,
// field-for-field: subjectFields(), conditionsFields(), the add/show
// endpoints and methods); accountAccessGrantsWithdrawFixture is the one
// executor_request_header_test.go already defines for this family's
// withdraw command, reused here rather than duplicated.

var accountAccessGrantsAddFixture = manifest.Command{
	Command:  "account/access/grants/add",
	Method:   http.MethodPost,
	Endpoint: "/:aid/account/access/grants",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "subject.kind", Type: "string", Required: true},
		{Name: "subject.id", Type: "string", Required: true},
		{Name: "scope", Type: "string", Required: true},
		{Name: "actions", Type: "array", ItemType: "string", Required: true},
		{Name: "conditions.source_ranges", Type: "array", ItemType: "string"},
		{Name: "conditions.window.start_min", Type: "integer"},
		{Name: "conditions.window.end_min", Type: "integer"},
		{Name: "not_before", Type: "string"},
		{Name: "expires_at", Type: "string"},
	}},
}

var accountAccessGrantsShowFixture = manifest.Command{
	Command:  "account/access/grants/show",
	Method:   http.MethodGet,
	Endpoint: "/:aid/account/access/grants/:grant",
	Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "grant", Type: "string", Required: true, Positional: true},
	}},
}

// grantDynamicPathCase is one row of the loopback table below: the exact
// published fixture, its dispatch args/body, and what the dynamic
// executor's transport and credential-output guard must do with it.
type grantDynamicPathCase struct {
	cmdDef manifest.Command
	args   []string
	body   map[string]any
	// wantETagInOutput proves the fix for add/show (Finding 1194, first
	// half): InjectETagIntoCredentialResponse must no longer exclude this
	// family, so the response's ETag header reaches the printed output.
	wantETagInOutput bool
	// wantIfMatchHeader proves the published withdraw DELETE shape
	// (Finding 1194, second half): ifMatch travels as the If-Match
	// header, never the query string or the JSON body.
	wantIfMatchHeader string
}

// grantDynamicPathCases is keyed by manifest verb. grantFamilyVerbsNeedingTransportProof
// below drives the test from this map, so a verb named there with no row
// here fails immediately instead of silently skipping coverage.
var grantDynamicPathCases = map[string]grantDynamicPathCase{
	"add": {
		cmdDef: accountAccessGrantsAddFixture,
		body: map[string]any{
			"subject.kind": "account_api_key",
			"subject.id":   "key_abc123",
			"scope":        "acct/acct1",
			"actions":      []string{"apps.deploy"},
		},
		wantETagInOutput: true,
	},
	"show": {
		cmdDef:           accountAccessGrantsShowFixture,
		args:             []string{"g1"},
		body:             map[string]any{"grant": "g1"},
		wantETagInOutput: true,
	},
	"withdraw": {
		cmdDef:            accountAccessGrantsWithdrawFixture,
		args:              []string{"g1"},
		body:              map[string]any{"grant": "g1", "ifMatch": `"etag-value-0"`},
		wantIfMatchHeader: `"etag-value-0"`,
	},
}

// grantFamilyVerbsNeedingTransportProof are the exact account/access/grants
// verbs Finding 1194 named: add and show lost their ETag, withdraw needed
// the published DELETE header/query proof. A verb added here with no
// matching grantDynamicPathCases row fails
// TestAccountAccessGrantsDynamicPathTransport immediately, rather than
// silently carrying no coverage.
var grantFamilyVerbsNeedingTransportProof = []string{"add", "show", "withdraw"}

func TestAccountAccessGrantsDynamicPathTransport(t *testing.T) {
	for _, verb := range grantFamilyVerbsNeedingTransportProof {
		tc, ok := grantDynamicPathCases[verb]
		if !ok {
			t.Fatalf("account/access/grants verb %q has no dynamic-path transport case", verb)
		}
		t.Run(verb, func(t *testing.T) {
			var gotHeaders http.Header
			var gotQuery string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeaders = r.Header.Clone()
				gotQuery = r.URL.RawQuery
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("ETag", `"grant-etag-1"`)
				w.WriteHeader(http.StatusOK)
				if tc.cmdDef.Method == http.MethodDelete {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"g1","scope":"acct/acct1"}`))
			}))
			t.Cleanup(srv.Close)

			cfg := &config.Config{AccountID: "acct1"}
			respBody, headers, err := NewExecutor(srv.URL).dispatch(tc.cmdDef, tc.args, tc.body, "", cfg, "test-token")
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}

			if tc.wantIfMatchHeader != "" {
				if got := gotHeaders.Get("If-Match"); got != tc.wantIfMatchHeader {
					t.Errorf("If-Match header = %q, want %q", got, tc.wantIfMatchHeader)
				}
				if gotQuery != "" {
					t.Errorf("ifMatch must never reach the query string, got: %q", gotQuery)
				}
				if _, present := gotBody["ifMatch"]; present {
					t.Errorf("ifMatch must never reach the JSON body, got body: %v", gotBody)
				}
			}

			if tc.wantETagInOutput {
				if err := GuardCredentialResponse(tc.cmdDef, respBody); err != nil {
					t.Fatalf("GuardCredentialResponse refused an ordinary grant response: %v", err)
				}
				withETag, err := InjectETagIntoCredentialResponse(tc.cmdDef, respBody, headers)
				if err != nil {
					t.Fatalf("InjectETagIntoCredentialResponse: %v", err)
				}
				var got map[string]any
				if err := json.Unmarshal(withETag, &got); err != nil {
					t.Fatalf("output not JSON: %v\n%s", err, withETag)
				}
				if got["etag"] != `"grant-etag-1"` {
					t.Errorf("%s output carries no etag field, got: %v", verb, got)
				}
			}
		})
	}
}
