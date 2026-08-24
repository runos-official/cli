package deploy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestDeployVCS_BodyCarriesBuildArgsCli pins the VCS deploy POST body
// shape: when the CLI passes `buildArgsCli` entries through DeployVCS,
// the wire body includes them as a structured `[{key,value}]` array
// alongside `sha` and `configPath`. Conductor (story 59) reads from
// this field. When no entries are supplied the field is OMITTED so the
// body is byte-equivalent to the pre-feature shape. Objective 40.
func TestDeployVCS_BodyCarriesBuildArgsCli(t *testing.T) {
	t.Run("with entries", func(t *testing.T) {
		var captured []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, _ = readAllBody(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jobId":"job-1"}`))
		}))
		t.Cleanup(srv.Close)

		svc := &Service{
			baseURL:    srv.URL,
			httpClient: &http.Client{Timeout: 5 * time.Second},
			token:      "t",
			aid:        "aid",
			cid:        "cid",
		}
		entries := []BuildArgCliEntry{
			{Key: "NEXT_PUBLIC_APP_VERSION", Value: "1.2.3"},
			{Key: "NODE_ENV", Value: "production"},
		}
		if _, err := svc.DeployVCS("app01", strings.Repeat("a", 40), "apps/web/runos.yaml", entries); err != nil {
			t.Fatalf("DeployVCS: %v", err)
		}

		var body map[string]any
		if err := json.Unmarshal(captured, &body); err != nil {
			t.Fatalf("body not JSON: %v\n%s", err, captured)
		}
		if body["sha"] != strings.Repeat("a", 40) {
			t.Errorf("body sha = %v, want 40 'a's", body["sha"])
		}
		if body["configPath"] != "apps/web/runos.yaml" {
			t.Errorf("body configPath = %v, want apps/web/runos.yaml", body["configPath"])
		}
		raw, ok := body["buildArgsCli"].([]any)
		if !ok {
			t.Fatalf("buildArgsCli missing or not an array; got %T: %v", body["buildArgsCli"], body["buildArgsCli"])
		}
		if len(raw) != 2 {
			t.Fatalf("buildArgsCli length = %d, want 2", len(raw))
		}
		first := raw[0].(map[string]any)
		if first["key"] != "NEXT_PUBLIC_APP_VERSION" || first["value"] != "1.2.3" {
			t.Errorf("buildArgsCli[0] = %v, want {key:NEXT_PUBLIC_APP_VERSION, value:1.2.3}", first)
		}
	})

	t.Run("argless deploy omits the field (back-compat)", func(t *testing.T) {
		var captured []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured, _ = readAllBody(r)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jobId":"job-2"}`))
		}))
		t.Cleanup(srv.Close)

		svc := &Service{
			baseURL:    srv.URL,
			httpClient: &http.Client{Timeout: 5 * time.Second},
			token:      "t",
			aid:        "aid",
			cid:        "cid",
		}
		if _, err := svc.DeployVCS("app01", strings.Repeat("a", 40), "", nil); err != nil {
			t.Fatalf("DeployVCS: %v", err)
		}
		if strings.Contains(string(captured), "buildArgsCli") {
			t.Errorf("argless body must omit buildArgsCli; got: %s", captured)
		}
	})
}

func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}

// Regression test for I2-4e (TEST_LOG.md): hostFromLink reduces a
// network-access link to its bare fqdn, which the gate diffs against
// the local yaml's `domain:` set. Bad URLs degrade gracefully (return
// the input verbatim) so a malformed link never crashes the gate.
func TestHostFromLink(t *testing.T) {
	cases := map[string]string{
		"https://app-appid2-3000.mycluster2.myacct.dev.runos.xyz": "app-appid2-3000.mycluster2.myacct.dev.runos.xyz",
		"https://my-custom.example.com":                           "my-custom.example.com",
		"https://my-custom.example.com:8443":                      "my-custom.example.com",
		"https://my-custom.example.com/path?q=1":                  "my-custom.example.com",
		"http://example.com":                                      "example.com",
		"":                                                        "",
		"not-a-url-just-a-host.example.com":                       "not-a-url-just-a-host.example.com",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got := hostFromLink(in)
			if got != want {
				t.Errorf("hostFromLink(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// Regression test for I2-4e”' (TEST_LOG.md): the gate sources
// custom-domain state from /:aid/domains, NOT /:aid/:cid/apps/:id/
// network-access. network-access only carries auto-assigned
// (RUNOS_PUBLIC_*) and K8s internal (IN_CLUSTER_*) entries, never the
// user's custom domains, so round-5's filter went from "too many
// entries" to "zero entries on real removals." This test pins the
// correct source endpoint + the targetIngressUrl-based per-app
// filter.
func TestGetAppCustomDomains_FiltersAndDedupes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Pin the endpoint shape: /:aid/domains, account-wide.
		if !strings.HasSuffix(r.URL.Path, "/aid/domains") {
			http.Error(w, "wrong endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		domains := []Domain{
			// Domains attached to app `appid2` (osid `app-appid2`).
			{ID: "d1", Fqdn: "app.example.com", TargetIngressURL: "http://app-appid2.app-appid2.svc.cluster.local:3000"},
			{ID: "d2", Fqdn: "alt.example.com", TargetIngressURL: "https://app-appid2-3000.mycluster2.aid.dev.runos.xyz"},
			// Different app: same prefix `app-hmx9oa` must NOT match
			// our `app-appid2` osid (token-boundary check).
			{ID: "d3", Fqdn: "other.example.com", TargetIngressURL: "http://app-hmx9oa.app-hmx9oa.svc.cluster.local:3000"},
			// Different app entirely.
			{ID: "d4", Fqdn: "elsewhere.example.com", TargetIngressURL: "http://app-zzzzz.app-zzzzz.svc.cluster.local:80"},
			// Duplicate fqdn collapses (same domain entered twice).
			{ID: "d5", Fqdn: "app.example.com", TargetIngressURL: "http://app-appid2.app-appid2.svc.cluster.local:8080"},
			// Empty fqdn skipped.
			{ID: "d6", Fqdn: "", TargetIngressURL: "http://app-appid2.app-appid2.svc.cluster.local:3000"},
		}
		_ = json.NewEncoder(w).Encode(domains)
	}))
	t.Cleanup(srv.Close)

	svc := &Service{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		token:      "t",
		aid:        "aid",
		cid:        "cid",
	}

	got, err := svc.GetAppCustomDomains("appid2")
	if err != nil {
		t.Fatalf("GetAppCustomDomains: %v", err)
	}
	want := []string{"app.example.com", "alt.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// I2-4e”' partner: empty account-wide list returns no domains.
func TestGetAppCustomDomains_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Domain{})
	}))
	t.Cleanup(srv.Close)

	svc := &Service{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		token:      "t",
		aid:        "aid",
		cid:        "cid",
	}

	got, err := svc.GetAppCustomDomains("appid2")
	if err != nil {
		t.Fatalf("GetAppCustomDomains: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}
}

// I2-4e”' partner: pure pin on the token-boundary matcher. Catches
// regressions where a future change to targetIngressMatchesOSID
// re-introduces prefix-only matching.
func TestTargetIngressMatchesOSID(t *testing.T) {
	cases := []struct {
		target string
		osid   string
		want   bool
	}{
		// Match: osid as full hostname segment.
		{"http://app-appid2.app-appid2.svc.cluster.local:3000", "app-appid2", true},
		{"https://app-appid2-3000.mycluster2.aid.dev.runos.xyz", "app-appid2", true},
		// Match: osid at end of string.
		{"http://app-appid2", "app-appid2", true},
		// No match: different prefix-overlapping app.
		{"http://app-hmx9oa.app-hmx9oa.svc.cluster.local:3000", "app-appid2", false},
		// No match: prefixed by another label without delimiter.
		{"http://xapp-appid2.svc.cluster.local", "app-appid2", false},
		// No match: completely different app.
		{"http://app-zzzzz.app-zzzzz.svc.cluster.local", "app-appid2", false},
		// Edge cases.
		{"", "app-appid2", false},
		{"http://app-appid2", "", false},
	}
	for _, c := range cases {
		t.Run(c.target+"_vs_"+c.osid, func(t *testing.T) {
			got := targetIngressMatchesOSID(c.target, c.osid)
			if got != c.want {
				t.Errorf("targetIngressMatchesOSID(%q, %q) = %v, want %v", c.target, c.osid, got, c.want)
			}
		})
	}
}

// I27-Z regression: DeployVCS must return a typed *APIError (not a plain
// fmt.Errorf) on conductor 4xx so cmd/apps_pull.go:emitJSONError can
// flatten the conductor body into the outer --json envelope. Pre-fix,
// the bad-SHA error came back as a fmt.Errorf wrapping "API error (400):
// {\"error\":\"...\"}" and the CLI's outer JSON ended up doubly-nested:
// `{"error":"failed to trigger VCS deploy: API error (400): {\"error\":..."}`.
// Now: errors.As resolves to *APIError with the raw body intact, so the
// flattener writes `{"error":"<inner msg>", "statusCode": 400}`.
func TestDeployVCS_ReturnsTypedAPIErrorOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/apps/app01/deploy") {
			http.Error(w, "wrong endpoint: "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Commit 'aaaa' not found in repo@main"}`))
	}))
	t.Cleanup(srv.Close)

	svc := &Service{
		baseURL:    srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		token:      "t",
		aid:        "aid",
		cid:        "cid",
	}

	_, err := svc.DeployVCS("app01", "aaaa", "", nil)
	if err == nil {
		t.Fatalf("expected error on 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected errors.As to match *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if !strings.Contains(string(apiErr.Body), "Commit 'aaaa' not found") {
		t.Errorf("Body lost inner error message: %q", apiErr.Body)
	}
}

// I27-AG regression: conductor 17.7.0's envelope-everywhere migration
// wrapped `apps/:id/dependencies` in `{dependencies: [...]}`. The CLI's
// `GetAppDependencies` decoder used to unmarshal the body directly into
// a `[]AppDependency`, which rejected the new shape with `cannot
// unmarshal object into Go value of type []deploy.AppDependency`.
// Pre-flight through `unwrapArrayEnvelopeDeploy` lets both shapes
// round-trip during the migration window. Same fix pattern as iter-26
// I26-O env-vars envelope handling.
func TestGetAppDependencies_AcceptsEnvelope(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{
			name: "conductor 17.7.0 envelope",
			body: `{"dependencies":[{"alias":"api-db","id":"mysvc","type":"postgresql"},{"alias":"shared-cache","id":"mysvc2","type":"valkey"}]}`,
			want: 2,
		},
		{
			name: "legacy bare array (migration window back-compat)",
			body: `[{"alias":"api-db","id":"mysvc","type":"postgresql"}]`,
			want: 1,
		},
		{
			name: "envelope with empty array",
			body: `{"dependencies":[]}`,
			want: 0,
		},
		{
			name: "legacy empty bare array",
			body: `[]`,
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/apps/app01/dependencies") {
					http.Error(w, "wrong endpoint: "+r.URL.Path, http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			svc := &Service{
				baseURL:    srv.URL,
				httpClient: &http.Client{Timeout: 5 * time.Second},
				token:      "t",
				aid:        "aid",
				cid:        "cid",
			}

			deps, err := svc.GetAppDependencies("app01")
			if err != nil {
				t.Fatalf("GetAppDependencies: %v", err)
			}
			if len(deps) != tc.want {
				t.Errorf("got %d deps, want %d (body: %s)", len(deps), tc.want, tc.body)
			}
		})
	}
}

// TestParseAccountDomainsResponse pins issue 70: every `runos deploy`
// printed "Warning: domain-removal gate skipped (fetch failed: ...)"
// because the parser hard-coded the legacy bare-array shape, but
// conductor's `/:aid/domains` migrated to the envelope `{domains:[...]}`
// in the iter-27 envelope sweep. The fix accepts both shapes so the
// gate fires through the conductor migration window and beyond.
func TestParseAccountDomainsResponse(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{
			name: "iter-27 envelope shape",
			body: `{"domains":[{"id":"d1","fqdn":"a.example.com","targetIngressUrl":"app-cshpj"},{"id":"d2","fqdn":"b.example.com","targetIngressUrl":"app-cshpj"}]}`,
			want: 2,
		},
		{
			name: "legacy bare array",
			body: `[{"id":"d1","fqdn":"a.example.com","targetIngressUrl":"app-cshpj"}]`,
			want: 1,
		},
		{
			name: "envelope with empty array",
			body: `{"domains":[]}`,
			want: 0,
		},
		{
			name: "legacy empty bare array",
			body: `[]`,
			want: 0,
		},
		{
			name:    "malformed json refused",
			body:    `not json`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAccountDomainsResponse([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAccountDomainsResponse: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d domains, want %d (body: %s)", len(got), tc.want, tc.body)
			}
		})
	}
}

// TestParseEnvVarsResponse_DeployCopy mirrors the apps-package test:
// the deploy package keeps its own copy of the parser so the
// internal/deploy → internal/apps dependency boundary stays one-way.
// Regression target: I26-O.
func TestParseEnvVarsResponse_DeployCopy(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[string]string
	}{
		{"envelope", `{"envVars":{"K":"v"}}`, map[string]string{"K": "v"}},
		{"envelope empty", `{"envVars":{}}`, map[string]string{}},
		{"envelope null inner", `{"envVars":null}`, map[string]string{}},
		{"legacy bare", `{"K":"v"}`, map[string]string{"K": "v"}},
		{"legacy empty", `{}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEnvVarsResponse([]byte(tc.body))
			if err != nil {
				t.Fatalf("parseEnvVarsResponse: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
