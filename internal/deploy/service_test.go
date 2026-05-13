package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Regression test for I2-4e (TEST_LOG.md): hostFromLink reduces a
// network-access link to its bare fqdn, which the gate diffs against
// the local yaml's `domain:` set. Bad URLs degrade gracefully (return
// the input verbatim) so a malformed link never crashes the gate.
func TestHostFromLink(t *testing.T) {
	cases := map[string]string{
		"https://app-appid2-3000.mycluster2.myacct.dev.runos.xyz":         "app-appid2-3000.mycluster2.myacct.dev.runos.xyz",
		"https://my-custom.example.com":                          "my-custom.example.com",
		"https://my-custom.example.com:8443":                     "my-custom.example.com",
		"https://my-custom.example.com/path?q=1":                 "my-custom.example.com",
		"http://example.com":                                     "example.com",
		"":                                                       "",
		"not-a-url-just-a-host.example.com":                      "not-a-url-just-a-host.example.com",
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

// Regression test for I2-4e''' (TEST_LOG.md): the gate sources
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

// I2-4e''' partner: empty account-wide list returns no domains.
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

// I2-4e''' partner: pure pin on the token-boundary matcher. Catches
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
