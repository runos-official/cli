package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/roidapi"
)

// explainCredentialVerdict is the one place that decides whether a
// response ever renders as "allowed". AC3 (story 345): conditional,
// indeterminate/unavailable and legacy non-authoritative results must
// never print an unconditional allow, even when the legacy compatibility
// path would usually allow the action.
func TestExplainCredentialVerdictOnlyAllowsAnAuthoritativePermit(t *testing.T) {
	for _, tc := range []struct {
		name              string
		outcome           string
		authoritativeness string
		want              string
	}{
		{"authoritative permit", "permit", "authoritative", "allowed"},
		{"authoritative deny", "deny", "authoritative", "denied"},
		{"conditional needs", "needs", "conditional", "conditional"},
		{"conditional deny stays denied", "deny", "conditional", "denied"},
		{"unavailable", "unavailable", "authoritative", "not determined"},
		{"legacy permit stays non-authoritative", "permit", "legacy_non_authoritative", "non-authoritative"},
		{"legacy deny stays non-authoritative", "deny", "legacy_non_authoritative", "non-authoritative"},
		// Defensive: the published contract pairs permit only with
		// authoritative, but a non-authoritative permit must still never
		// render as allowed if the server ever sends one.
		{"conditional permit never allowed", "permit", "conditional", "not determined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := explainCredentialResponse{Outcome: tc.outcome, Authoritativeness: tc.authoritativeness}
			got := explainCredentialVerdict(r)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("verdict(%s, %s) = %q, want it to contain %q", tc.outcome, tc.authoritativeness, got, tc.want)
			}
			if tc.want != "allowed" && strings.Contains(got, "allowed") {
				t.Fatalf("verdict(%s, %s) = %q: a non-permit result said allowed", tc.outcome, tc.authoritativeness, got)
			}
		})
	}
}

func sampleExplainResponse() explainCredentialResponse {
	return explainCredentialResponse{
		Version: "runos.credential-explanation.v1",
		Credential: explainCredentialRef{
			Version: "runos.credential-reference.v1", CredentialType: "personal_pat",
			CredentialID: "pat_abc123", PrincipalKind: "person", PrincipalID: "p_1",
			LegacyClassification: "none",
		},
		Action: "clusters.read", Scope: "acct/acct-1/cluster/c1",
		Outcome: "permit", Code: "policy.permit", Reason: "granted by role viewer",
		PolicyVersion: 7, EvaluatedAt: "2026-11-19T18:00:00Z",
		PermissionSource:  explainPermissionSource{Kind: "current_user_intersection", RoleIDs: []string{"viewer"}, GrantIDs: []string{"g_1"}},
		Authoritativeness: "authoritative", LifecycleState: "active", PolicyFreshness: "current",
	}
}

func TestRenderExplainCredentialShowsTheSafeFields(t *testing.T) {
	var buf bytes.Buffer
	renderExplainCredential(&buf, sampleExplainResponse())
	out := buf.String()
	for _, want := range []string{
		"pat_abc123", "personal_pat", "p_1", "clusters.read", "acct/acct-1/cluster/c1",
		"allowed", "granted by role viewer", "viewer", "g_1", "7", "current", "2026-11-19T18:00:00Z", "active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRenderExplainCredentialNamesMissingEvidenceAsConditionalNeverAllowed(t *testing.T) {
	r := sampleExplainResponse()
	r.Outcome, r.Authoritativeness = "needs", "conditional"
	r.Conditions = []string{"missing proof of a fresh sign-in", "source range required"}
	var buf bytes.Buffer
	renderExplainCredential(&buf, r)
	out := buf.String()
	if strings.Contains(out, "allowed") {
		t.Fatalf("a conditional result said allowed:\n%s", out)
	}
	for _, want := range []string{"conditional", "missing proof of a fresh sign-in", "source range required"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRenderExplainCredentialKeepsALegacyCompatibilityPermitNonAuthoritative(t *testing.T) {
	r := sampleExplainResponse()
	r.Authoritativeness = "legacy_non_authoritative"
	r.LifecycleState = "legacy"
	r.Credential.LegacyClassification = "roid_account_role"
	var buf bytes.Buffer
	renderExplainCredential(&buf, r)
	out := buf.String()
	if strings.Contains(out, "allowed") {
		t.Fatalf("a legacy non-authoritative permit said allowed:\n%s", out)
	}
	if !strings.Contains(out, "non-authoritative") {
		t.Errorf("output does not say non-authoritative:\n%s", out)
	}
}

func TestRenderExplainCredentialShowsExpiry(t *testing.T) {
	r := sampleExplainResponse()
	r.LifecycleState, r.Outcome = "expired", "deny"
	exp := "2026-01-01T00:00:00Z"
	r.ExpiresAt = &exp
	var buf bytes.Buffer
	renderExplainCredential(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "expired") || !strings.Contains(out, exp) || !strings.Contains(out, "denied") {
		t.Errorf("output lacks expiry or denial:\n%s", out)
	}
}

// fakeExplain answers POST /v1/accounts/{acct}/access/explain once with a
// fixed status and body, and records the request body it saw.
func fakeExplain(t *testing.T, status int, body string) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.Bytes()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotBody
}

func explainTarget(srv *httptest.Server) *accessTarget {
	client := roidapi.New(srv.URL, func() (string, error) { return "at_test", nil })
	return &accessTarget{client: client, acct: "acct-1"}
}

const samplePermitBody = `{"version":"runos.credential-explanation.v1","credential":{"version":"runos.credential-reference.v1","credentialType":"personal_pat","credentialId":"pat_abc123","principalKind":"person","principalId":"p_1","legacyClassification":"none"},"action":"clusters.read","scope":"acct/acct-1/cluster/c1","outcome":"permit","code":"policy.permit","reason":"granted by role viewer","policyVersion":7,"evaluatedAt":"2026-11-19T18:00:00Z","permissionSource":{"kind":"current_user_intersection","roleIds":["viewer"],"grantIds":["g_1"],"restrictionActions":[]},"conditions":[],"authoritativeness":"authoritative","lifecycleState":"active","expiresAt":null,"policyFreshness":"current"}`

func TestExplainCredentialSendsTheExactRequestContract(t *testing.T) {
	srv, gotBody := fakeExplain(t, http.StatusOK, samplePermitBody)
	tg := explainTarget(srv)
	resp, _, err := tg.explainCredential("pat_abc123", "clusters.read", "acct/acct-1/cluster/c1")
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(*gotBody, &sent); err != nil {
		t.Fatalf("request body: %v (%s)", err, *gotBody)
	}
	if sent["version"] != "runos.credential-explanation.v1" || sent["credentialId"] != "pat_abc123" ||
		sent["action"] != "clusters.read" || sent["scope"] != "acct/acct-1/cluster/c1" {
		t.Fatalf("request body = %v", sent)
	}
	ctx, ok := sent["context"].(map[string]any)
	if !ok || len(ctx) != 0 {
		t.Fatalf("context = %v, want an empty object: the CLI never fabricates auth, approval, or network evidence", sent["context"])
	}
	if resp.Outcome != "permit" || resp.Authoritativeness != "authoritative" {
		t.Fatalf("resp = %+v", resp)
	}
	if !strings.Contains(explainCredentialVerdict(*resp), "allowed") {
		t.Fatalf("verdict = %q, want allowed", explainCredentialVerdict(*resp))
	}
}

func TestExplainCredentialNeverDecodesAConditionalOrLegacyResultAsAllowed(t *testing.T) {
	cases := map[string]string{
		"needs":       `{"version":"runos.credential-explanation.v1","credential":{"version":"runos.credential-reference.v1","credentialType":"account_api_key","credentialId":"key_x","principalKind":"account_api_key","principalId":"k_1","legacyClassification":"none"},"action":"apps.deploy","scope":"acct/acct-1","outcome":"needs","code":"authz.needs","reason":"missing context","policyVersion":3,"evaluatedAt":"2026-11-19T18:00:00Z","permissionSource":{"kind":"independent_grants","roleIds":[],"grantIds":["g_2"],"restrictionActions":[]},"conditions":["missing proof of a fresh sign-in"],"authoritativeness":"conditional","lifecycleState":"active","expiresAt":null,"policyFreshness":"current"}`,
		"legacy":      `{"version":"runos.credential-explanation.v1","credential":{"version":"runos.credential-reference.v1","credentialType":"legacy_roid_account_key","credentialId":"key_legacy","principalKind":"account_api_key","principalId":"k_2","legacyClassification":"roid_account_role"},"action":"apps.deploy","scope":"acct/acct-1","outcome":"permit","code":"legacy.compat","reason":"legacy account-role key; compatibility path only","policyVersion":0,"evaluatedAt":"2026-11-19T18:00:00Z","permissionSource":{"kind":"legacy_role","roleIds":[],"grantIds":[],"restrictionActions":[]},"conditions":[],"authoritativeness":"legacy_non_authoritative","lifecycleState":"legacy","expiresAt":null,"policyFreshness":"unavailable"}`,
		"unavailable": `{"version":"runos.credential-explanation.v1","credential":{"version":"runos.credential-reference.v1","credentialType":"personal_pat","credentialId":"pat_abc123","principalKind":"person","principalId":"p_1","legacyClassification":"none"},"action":"clusters.read","scope":"acct/acct-1","outcome":"unavailable","code":"store_unavailable","reason":"a dependency could not be read","policyVersion":7,"evaluatedAt":"2026-11-19T18:00:00Z","permissionSource":{"kind":"current_user_intersection","roleIds":[],"grantIds":[],"restrictionActions":[]},"conditions":[],"authoritativeness":"authoritative","lifecycleState":"active","expiresAt":null,"policyFreshness":"current"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := fakeExplain(t, http.StatusOK, body)
			tg := explainTarget(srv)
			resp, _, err := tg.explainCredential("x", "a", "acct/acct-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := explainCredentialVerdict(*resp); strings.Contains(got, "allowed") {
				t.Fatalf("%s: verdict = %q", name, got)
			}
		})
	}
}

// A refusal for an absent, foreign, or otherwise invisible credential must
// carry no more detail than the server sent: the server's contract makes
// these indistinguishable on purpose (non-enumerating 404), and the CLI
// must not invent a distinction on top.
func TestExplainCredentialRevealsNothingMoreOnAnUnauthorizedOrAbsentCredential(t *testing.T) {
	notFound := `{"error":{"code":"not_found"}}`
	for _, id := range []string{"pat_absent", "pat_foreign"} {
		t.Run(id, func(t *testing.T) {
			srv, _ := fakeExplain(t, http.StatusNotFound, notFound)
			tg := explainTarget(srv)
			_, _, err := tg.explainCredential(id, "clusters.read", "acct/acct-1")
			if err == nil {
				t.Fatal("want a refusal")
			}
			if !strings.Contains(err.Error(), "not_found") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// --json must stay faithful to the server response: the raw body comes
// back untouched for the caller to print, never reshaped or summarized.
func TestExplainCredentialRawResultIsFaithfulToTheServerResponse(t *testing.T) {
	srv, _ := fakeExplain(t, http.StatusOK, samplePermitBody)
	tg := explainTarget(srv)
	_, res, err := tg.explainCredential("pat_abc123", "clusters.read", "acct/acct-1")
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	_ = json.Unmarshal([]byte(samplePermitBody), &want)
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatalf("res.Body is not the server's JSON: %v", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("got %s, want %s", gotJSON, wantJSON)
	}
}
