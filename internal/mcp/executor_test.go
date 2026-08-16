package mcp

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// Bug 86 regression: the MCP executor's getAuthToken must accept a
// stored PAT (cfg.APIKey) and RUNOS_API_KEY, not hard-require Firebase.
// Pre-fix it returned "not authenticated" for any PAT-only config, so
// every MCP tool failed for a PAT user even though the same PAT worked
// for ordinary CLI commands. We assert only the PAT and no-credential
// paths here; the Firebase path round-trips to Google and isn't unit
// testable without a live token exchange.
func TestGetAuthTokenPAT(t *testing.T) {
	e := NewCommandExecutor(nil, "https://example.test")

	t.Run("stored PAT, no firebase", func(t *testing.T) {
		t.Setenv(auth.APIKeyEnvVar, "")
		cfg := &config.Config{APIKey: "stored-pat-abc"}
		got, err := e.getAuthToken(cfg)
		if err != nil {
			t.Fatalf("getAuthToken returned error for stored PAT: %v", err)
		}
		if got != "stored-pat-abc" {
			t.Errorf("getAuthToken = %q, want stored PAT", got)
		}
	})

	t.Run("env PAT overrides", func(t *testing.T) {
		t.Setenv(auth.APIKeyEnvVar, "env-pat-xyz")
		cfg := &config.Config{APIKey: "stored-pat-abc"}
		got, err := e.getAuthToken(cfg)
		if err != nil {
			t.Fatalf("getAuthToken returned error for env PAT: %v", err)
		}
		if got != "env-pat-xyz" {
			t.Errorf("getAuthToken = %q, want env PAT", got)
		}
	})

	t.Run("no credentials errors", func(t *testing.T) {
		t.Setenv(auth.APIKeyEnvVar, "")
		cfg := &config.Config{}
		if _, err := e.getAuthToken(cfg); err == nil {
			t.Error("getAuthToken returned nil error for empty config, want not-authenticated error")
		}
	})
}

// I4-K MCP path regression: the MCP executor must opt into
// `?merge=true` for `apps/update` exactly the way the cobra/dynacmd
// path does. Pre-fix, the MCP path wrote partial-PATCH bodies (e.g.
// `{replicas: 4}`) to the bare endpoint and the conductor's
// desired-state mode wiped healthCheck/metrics. Pinned per shape so
// neither implementation can drift from the other silently.
func TestAppendMergeQueryMCP(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/myacct/mycluster2/apps/abc12", "/myacct/mycluster2/apps/abc12?merge=true"},
		{"/myacct/mycluster2/apps/abc12?", "/myacct/mycluster2/apps/abc12?&merge=true"},
		{"/myacct/mycluster2/apps/abc12?foo=bar", "/myacct/mycluster2/apps/abc12?foo=bar&merge=true"},
		// Idempotent.
		{"/myacct/mycluster2/apps/abc12?merge=true", "/myacct/mycluster2/apps/abc12?merge=true"},
		{"/myacct/mycluster2/apps/abc12?foo=bar&merge=true", "/myacct/mycluster2/apps/abc12?foo=bar&merge=true"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := appendMergeQuery(c.in); got != c.want {
				t.Errorf("appendMergeQuery(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExtractCID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain CID without cluster name",
			input: "abc123",
			want:  "abc123",
		},
		{
			name:  "CID with cluster name in parentheses",
			input: "xyz (Cluster Name)",
			want:  "xyz",
		},
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "single character CID",
			input: "x",
			want:  "x",
		},
		{
			name:  "CID with multiple spaces",
			input: "abc some extra info",
			want:  "abc",
		},
		{
			name:  "CID starting with space returns full string (idx=0 not > 0)",
			input: " leading",
			want:  " leading",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCID(tt.input)
			if got != tt.want {
				t.Errorf("extractCID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// substituteFields replicates the field substitution logic from buildEndpointWithCID
// so we can test it without needing config/auth.
func substituteFields(endpoint string, args map[string]any, cmdDef *manifest.Command) string {
	result := endpoint
	if cmdDef.Input != nil {
		for _, field := range cmdDef.Input.Fields {
			val, ok := args[field.Name]
			if !ok {
				placeholder := ":" + field.Name
				if idx := strings.Index(result, placeholder); idx > 0 {
					prefix := result[:idx]
					parts := strings.Split(strings.Trim(prefix, "/"), "/")
					if len(parts) > 0 {
						entity := parts[len(parts)-1]
						if v, found := args[entity+"_"+field.Name]; found {
							val = v
							ok = true
						} else if singular := strings.TrimSuffix(entity, "s"); singular != entity {
							if v, found := args[singular+"_"+field.Name]; found {
								val = v
								ok = true
							}
						}
					}
				}
			}
			if ok {
				valStr := fmt.Sprintf("%v", val)
				escapedVal := url.PathEscape(valStr)
				result = strings.ReplaceAll(result, "{"+field.Name+"}", escapedVal)
				result = strings.ReplaceAll(result, ":"+field.Name, escapedVal)
			}
		}
	}
	return result
}

func TestSubstituteFields(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		args     map[string]any
		cmdDef   *manifest.Command
		want     string
	}{
		{
			name:     "exact field name match",
			endpoint: "/acct/cl/apps/:id/status",
			args:     map[string]any{"id": "appid4"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{{Name: "id", Positional: true}},
				},
			},
			want: "/acct/cl/apps/appid4/status",
		},
		{
			name:     "AI sends singular prefix (app_id instead of id)",
			endpoint: "/acct/cl/apps/:id/status",
			args:     map[string]any{"app_id": "appid4"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{{Name: "id", Positional: true}},
				},
			},
			want: "/acct/cl/apps/appid4/status",
		},
		{
			name:     "AI sends plural prefix (apps_id instead of id)",
			endpoint: "/acct/cl/apps/:id/status",
			args:     map[string]any{"apps_id": "appid4"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{{Name: "id", Positional: true}},
				},
			},
			want: "/acct/cl/apps/appid4/status",
		},
		{
			name:     "overrides endpoint with two path params",
			endpoint: "/acct/cl/apps/:id/overrides/:overrideId",
			args:     map[string]any{"app_id": "appid4", "overrideId": "ovr1"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{
						{Name: "id", Positional: true},
						{Name: "overrideId", Positional: true},
					},
				},
			},
			want: "/acct/cl/apps/appid4/overrides/ovr1",
		},
		{
			name:     "no match leaves placeholder intact",
			endpoint: "/acct/cl/apps/:id/status",
			args:     map[string]any{"something_else": "appid4"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{{Name: "id", Positional: true}},
				},
			},
			want: "/acct/cl/apps/:id/status",
		},
		{
			name:     "curly brace placeholder style",
			endpoint: "/acct/cl/services/postgresql/{id}/show",
			args:     map[string]any{"id": "svc1"},
			cmdDef: &manifest.Command{
				Input: &manifest.Input{
					Fields: []manifest.Field{{Name: "id", Positional: true}},
				},
			},
			want: "/acct/cl/services/postgresql/svc1/show",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substituteFields(tt.endpoint, tt.args, tt.cmdDef)
			if got != tt.want {
				t.Errorf("substituteFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

// I16-C regression: the MCP executor strips the "(Cluster Name)" suffix off
// args["cid"] before buildBody runs, so account-scoped POSTs that carry cid as
// a BODY field (e.g. cluster-domains/add, endpoint /:aid/cluster-domains)
// don't leak the display label onto the wire. Pre-fix, conductor received
// `cid: "mycluster2 (Cluster-2 mycluster2)"` in the POST body and silently accepted it,
// then failed downstream when no cluster with that bogus id existed.
func TestBuildBodyStripsClusterNameSuffixFromCID(t *testing.T) {
	e := &CommandExecutor{}
	cmdDef := &manifest.Command{
		Method:   "POST",
		Endpoint: "/:aid/cluster-domains",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "cid", Type: "string", Required: true},
				{Name: "zone", Type: "string", Required: true},
				{Name: "integrationId", Type: "string", Required: true},
				{Name: "isDefault", Type: "boolean"},
			},
		},
	}
	args := map[string]any{
		"cid":           "mycluster2 (Cluster-2 mycluster2)",
		"zone":          "example.com",
		"integrationId": "int-1",
	}
	// Mirror the canonicalization performed at the top of Execute (the fix).
	if cid, ok := args["cid"].(string); ok {
		args["cid"] = extractCID(cid)
	}

	body := e.buildBody(args, cmdDef)
	if got := body["cid"]; got != "mycluster2" {
		t.Fatalf("body[\"cid\"] = %q, want %q (display-label suffix leaked into wire body)", got, "mycluster2")
	}
	if got := body["zone"]; got != "example.com" {
		t.Errorf("body[\"zone\"] = %q, want %q", got, "example.com")
	}
	if got := body["integrationId"]; got != "int-1" {
		t.Errorf("body[\"integrationId\"] = %q, want %q", got, "int-1")
	}
}

// foreman #78 regression: empty-body 2xx responses (e.g. DELETE
// /account/api-keys/{id}) must surface as a meaningful success string,
// not "", so the resulting MCP text content block carries a non-empty
// `text` field and conformant clients accept the frame.
func TestIsEmptyBody(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"nil", nil, true},
		{"empty", []byte(""), true},
		{"whitespace only", []byte("  \n\t "), true},
		{"json null", []byte("null"), false},
		{"json object", []byte(`{"ok":true}`), false},
		{"plain text", []byte("ok"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isEmptyBody(c.in); got != c.want {
				t.Errorf("isEmptyBody(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// foreman #78 regression: the deterministic success message must
// always include both the command and the HTTP status text, never
// empty, so the MCP wrapper never produces a `{"type":"text"}` frame
// with no `text` field.
func TestEmptyBodySuccessMessage(t *testing.T) {
	cases := []struct {
		name    string
		command string
		status  string
		want    string
	}{
		{
			name:    "revoke 200 OK",
			command: "account/api-keys/revoke",
			status:  "200 OK",
			want:    "account/api-keys/revoke succeeded (200 OK)",
		},
		{
			name:    "204 no content",
			command: "apps/delete",
			status:  "204 No Content",
			want:    "apps/delete succeeded (204 No Content)",
		},
		{
			name:    "empty command falls back",
			command: "",
			status:  "200 OK",
			want:    "request succeeded (200 OK)",
		},
		{
			name:    "empty status falls back",
			command: "apps/delete",
			status:  "",
			want:    "apps/delete succeeded (200 OK)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := emptyBodySuccessMessage(c.command, c.status)
			if got != c.want {
				t.Errorf("emptyBodySuccessMessage(%q,%q) = %q, want %q", c.command, c.status, got, c.want)
			}
			if got == "" {
				t.Errorf("emptyBodySuccessMessage must never return empty string")
			}
		})
	}
}

// TestCIDIsAQueryParamOnAccountScopedEndpoints is the goal-19 A2
// regression. buildEndpointWithCID skipped the `cid` field for every
// GET/DELETE query string, on the assumption that cid is always a path
// segment. On an ACCOUNT-scoped endpoint (`/:aid/vm-usage`,
// `/:aid/vm-events`, `/:aid/config/get`) cid is a real filter, and
// dropping it silently widened every read to the whole account: measured
// 152 VMs reported where the caller asked about a cluster holding 8.
func TestCIDIsAQueryParamOnAccountScopedEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		field    string
		want     bool
	}{
		{"cid on a cluster-scoped endpoint is a path segment", "/:aid/:cid/vms", "cid", true},
		{"cid on an account-scoped endpoint is a filter", "/:aid/vm-usage", "cid", false},
		{"cid on an account-scoped events feed is a filter", "/:aid/vm-events", "cid", false},
		{"an ordinary field is never skipped", "/:aid/vm-usage", "from", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := skipFieldAsQueryParam(c.field, c.endpoint); got != c.want {
				t.Errorf("skipFieldAsQueryParam(%q, %q) = %v, want %v", c.field, c.endpoint, got, c.want)
			}
		})
	}
}
