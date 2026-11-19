package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// Review finding 1080: roid/internal/server/credentialexplain.go (commit
// 5acd44af) puts unmet requirements ("needs:...", "risk:unassessed"),
// already-satisfied conditions (suffix ":satisfied"), and plain
// restriction/evidence tags ("credential:...", "grant:...", "reason:...")
// in the same conditions array. conditionGroup must bucket each by that
// real vocabulary, not treat every tag as missing.
func TestConditionGroupBucketsByTheProducersOwnVocabulary(t *testing.T) {
	for _, tc := range []struct{ tag, want string }{
		{"grant:window:satisfied", "satisfied"},
		{"grant:mfa:satisfied", "satisfied"},
		{"account:network:satisfied", "satisfied"},
		{"needs:step_up", "unmet"},
		{"needs:assurance", "unmet"},
		{"risk:unassessed", "unmet"},
		{"credential:action_class:read", "evidence"},
		{"credential:source_range:192.0.2.0/24", "evidence"},
		{"grant:g_1", "evidence"},
		{"grant:source_range:198.51.100.0/24", "evidence"},
		{"reason:no_grant", "evidence"},
	} {
		if got := conditionGroup(tc.tag); got != tc.want {
			t.Errorf("conditionGroup(%q) = %q, want %q", tc.tag, got, tc.want)
		}
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
		Conditions:        []string{"grant:g_1", "grant:window:satisfied", "credential:action_class:read"},
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

// A satisfied condition on an authoritative permit must render under
// "Satisfied:", never "Missing or unmet:" (review finding 1080: the
// renderer used to label every condition tag as missing, so a satisfied
// permit's own output looked self-contradictory).
func TestRenderExplainCredentialPutsASatisfiedConditionUnderSatisfiedNotMissing(t *testing.T) {
	var buf bytes.Buffer
	renderExplainCredential(&buf, sampleExplainResponse())
	out := buf.String()
	satisfiedIdx := strings.Index(out, "Satisfied:")
	if satisfiedIdx < 0 {
		t.Fatalf("no Satisfied: section:\n%s", out)
	}
	if !strings.Contains(out[satisfiedIdx:], "grant:window:satisfied") {
		t.Errorf("grant:window:satisfied is not under Satisfied::\n%s", out)
	}
	if missingIdx := strings.Index(out, "Missing or unmet:"); missingIdx >= 0 && strings.Contains(out[missingIdx:satisfiedIdx], "satisfied") {
		t.Errorf("a satisfied tag leaked into Missing or unmet::\n%s", out)
	}
}

func TestRenderExplainCredentialNamesMissingEvidenceAsConditionalNeverAllowed(t *testing.T) {
	r := sampleExplainResponse()
	r.Outcome, r.Authoritativeness = "needs", "conditional"
	r.Conditions = []string{"needs:step_up", "needs:assurance"}
	var buf bytes.Buffer
	renderExplainCredential(&buf, r)
	out := buf.String()
	if strings.Contains(out, "allowed") {
		t.Fatalf("a conditional result said allowed:\n%s", out)
	}
	missingIdx := strings.Index(out, "Missing or unmet:")
	if missingIdx < 0 {
		t.Fatalf("no Missing or unmet: section:\n%s", out)
	}
	for _, want := range []string{"conditional", "needs:step_up", "needs:assurance"} {
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

// captureStdout (cmd/deploy_test.go) redirects os.Stdout for the
// duration of fn and returns what was written.

const samplePermitBody = `{"version":"runos.credential-explanation.v1","credential":{"version":"runos.credential-reference.v1","credentialType":"personal_pat","credentialId":"pat_abc123","principalKind":"person","principalId":"p_1","legacyClassification":"none"},"action":"clusters.read","scope":"acct/acct-1/cluster/c1","outcome":"permit","code":"policy.permit","reason":"granted by role viewer","policyVersion":7,"evaluatedAt":"2026-11-19T18:00:00Z","permissionSource":{"kind":"current_user_intersection","roleIds":["viewer"],"grantIds":["g_1"],"restrictionActions":[]},"conditions":["grant:g_1","grant:window:satisfied"],"authoritativeness":"authoritative","lifecycleState":"active","expiresAt":null,"policyFreshness":"current"}`

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

// Review finding 1083: exercise the command's real observable output for
// both --json and human mode, across permit, deny, missing-context
// (needs), legacy compatibility, unavailable, expiry, and PAT role loss -
// the exact set AC4 names - comparing structured and rendered output for
// each, not just the decoding helpers in isolation.
func TestRenderExplainCredentialToCoversEveryRequiredOutcome(t *testing.T) {
	roleLoss := sampleExplainResponse()
	roleLoss.Outcome, roleLoss.Reason, roleLoss.Code = "deny", "the role that granted this action was removed", "authz.denied"
	roleLoss.PermissionSource = explainPermissionSource{Kind: "current_user_intersection"}
	roleLoss.Conditions = []string{"reason:no_grant"}

	needsCtx := sampleExplainResponse()
	needsCtx.Outcome, needsCtx.Authoritativeness = "needs", "conditional"
	needsCtx.Conditions = []string{"needs:step_up"}

	legacyPermit := sampleExplainResponse()
	legacyPermit.Authoritativeness, legacyPermit.LifecycleState = "legacy_non_authoritative", "legacy"
	legacyPermit.Credential.LegacyClassification = "roid_account_role"

	unavailable := sampleExplainResponse()
	unavailable.Outcome, unavailable.PolicyFreshness, unavailable.Conditions = "unavailable", "unavailable", nil

	expired := sampleExplainResponse()
	expired.Outcome, expired.LifecycleState = "deny", "expired"
	exp := "2026-01-01T00:00:00Z"
	expired.ExpiresAt = &exp

	for name, resp := range map[string]explainCredentialResponse{
		"permit":                      sampleExplainResponse(),
		"deny_pat_role_loss":          roleLoss,
		"needs_missing_context":       needsCtx,
		"legacy_compatibility_permit": legacyPermit,
		"unavailable":                 unavailable,
		"expired":                     expired,
	} {
		t.Run(name, func(t *testing.T) {
			rawBody, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}

			var human bytes.Buffer
			if err := renderExplainCredentialTo(&human, false, resp, rawBody); err != nil {
				t.Fatal(err)
			}
			wantAllowed := resp.Outcome == "permit" && resp.Authoritativeness == "authoritative"
			if strings.Contains(human.String(), "allowed") != wantAllowed {
				t.Fatalf("human output allowed-ness = %v, want %v:\n%s", strings.Contains(human.String(), "allowed"), wantAllowed, human.String())
			}

			var jsonOut bytes.Buffer
			if err := renderExplainCredentialTo(&jsonOut, true, resp, rawBody); err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(jsonOut.Bytes(), &got); err != nil {
				t.Fatalf("--json output is not valid JSON: %v\n%s", err, jsonOut.String())
			}
			_ = json.Unmarshal(rawBody, &want)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("--json output not faithful: got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// Review finding 1083: a non-enumerating 404 must produce the identical
// CLI-observable refusal for an absent credential and for one the viewer
// has no right to inspect, with no extra CLI-added detail distinguishing
// them (an exact string comparison, not a shared substring, which could
// still pass if the two messages differed elsewhere).
func TestExplainCredentialRevealsNothingMoreOnAnUnauthorizedOrAbsentCredential(t *testing.T) {
	notFound := `{"error":{"code":"not_found"}}`
	var errs []string
	for _, id := range []string{"pat_absent", "pat_foreign"} {
		srv, _ := fakeExplain(t, http.StatusNotFound, notFound)
		tg := explainTarget(srv)
		_, _, err := tg.explainCredential(id, "clusters.read", "acct/acct-1")
		if err == nil {
			t.Fatalf("%s: want a refusal", id)
		}
		errs = append(errs, err.Error())
	}
	if errs[0] != errs[1] {
		t.Fatalf("absent and foreign gave different refusals: %q vs %q", errs[0], errs[1])
	}
	if !strings.Contains(errs[0], "not_found") {
		t.Fatalf("err = %q", errs[0])
	}
}

// Review finding 1083: the person path's request and output must still
// work exactly as before (FPL440 "Preserve person Explain").
func TestAccessExplainPersonRequestAndOutputUnchanged(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"person":"p_1","scope":"acct/acct-1","policy_version":7,"assumes":"","actions":[{"action":"clusters.read","class":"read","outcome":"permit","grant":"g_1","via":"group:ops"}]}`))
	}))
	t.Cleanup(srv.Close)
	tg := explainTarget(srv)

	q := url.Values{"person": {"p_1"}, "scope": {"acct/acct-1"}}
	res, err := tg.do(roidapi.Call{Op: "getEffectiveAccess", Query: q})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("person") != "p_1" || gotQuery.Get("scope") != "acct/acct-1" {
		t.Fatalf("query = %v", gotQuery)
	}
	out := captureStdout(t, func() {
		if err := tg.print(res, outputFields("array", "action", "class", "outcome", "needs", "grant", "via", "reason")); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{"clusters.read", "permit", "g_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
