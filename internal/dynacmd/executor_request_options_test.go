package dynacmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

func TestDispatchPreservesBodylessRequests(t *testing.T) {
	// The service show and account leave shapes mirror manifest commands.
	// The service action and non-service POST controls are synthetic.
	tests := []struct {
		name, command, method, endpoint, path, query, body, contentType string
		fields                                                          []manifest.Field
		input                                                           map[string]any
		options                                                         requestOptions
	}{
		{name: "service show", command: "services/valkey/{id}/show", method: http.MethodGet, endpoint: "/:aid/:cid/services/valkey/:id", path: "/acct1/cluster1/services/valkey/abc12", fields: []manifest.Field{{Name: "id", Positional: true}}, input: map[string]any{"id": "abc12"}},
		{name: "service show query", command: "services/valkey/{id}/show", method: http.MethodGet, endpoint: "/:aid/:cid/services/valkey/:id", path: "/acct1/cluster1/services/valkey/abc12", query: "detail=true", fields: []manifest.Field{{Name: "id", Positional: true}, {Name: "detail"}}, input: map[string]any{"id": "abc12", "detail": true}},
		{name: "account leave", command: "account/leave", method: http.MethodDelete, endpoint: "/:aid/account/membership", path: "/acct1/account/membership"},
		{name: "account leave query", command: "account/leave", method: http.MethodDelete, endpoint: "/:aid/account/membership", path: "/acct1/account/membership", query: "scope=current", fields: []manifest.Field{{Name: "scope"}}, input: map[string]any{"scope": "current"}},
		{name: "service action", command: "services/valkey/{id}/action", method: http.MethodPost, endpoint: "/:aid/:cid/services/valkey/:id/action", path: "/acct1/cluster1/services/valkey/abc12/action", fields: []manifest.Field{{Name: "id", Positional: true}}, input: map[string]any{"id": "abc12"}},
		{name: "non-service post", command: "jobs/{id}/action", method: http.MethodPost, endpoint: "/:aid/jobs/:id/action", path: "/acct1/jobs/abc12/action", fields: []manifest.Field{{Name: "id", Positional: true}}, input: map[string]any{"id": "abc12"}},
		{name: "ordinary post", command: "account/api-keys/add", method: http.MethodPost, endpoint: "/:aid/api-keys", path: "/acct1/api-keys", body: `{"name":"example"}`, contentType: "application/json", fields: []manifest.Field{{Name: "name"}}, input: map[string]any{"name": "example"}},
		{name: "explicit empty object", command: "services/valkey/add", method: http.MethodPost, endpoint: "/:aid/:cid/services/valkey", path: "/acct1/cluster1/services/valkey", body: `{}`, contentType: "application/json", input: map[string]any{}, options: requestOptions{includeEmptyJSONObject: true}},
		{name: "explicit option does not change GET", command: "services/valkey/list", method: http.MethodGet, endpoint: "/:aid/:cid/services/valkey", path: "/acct1/cluster1/services/valkey", options: requestOptions{includeEmptyJSONObject: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				raw, err := io.ReadAll(r.Body)
				if err != nil || r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != tc.query || string(raw) != tc.body || r.Header.Get("Content-Type") != tc.contentType {
					t.Errorf("request = %s %s?%s, body = %q, type = %q, error = %v", r.Method, r.URL.Path, r.URL.RawQuery, raw, r.Header.Get("Content-Type"), err)
				}
				w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			command := manifest.Command{Command: tc.command, Method: tc.method, Endpoint: tc.endpoint, Input: &manifest.Input{Fields: tc.fields}}
			cfg := &config.Config{AccountID: "acct1", DefaultClusterID: "cluster1"}
			_, err := NewExecutor(srv.URL).dispatchWithOptions(command, nil, tc.input, "cluster1", cfg, "test-token", tc.options)
			if err != nil || calls != 1 {
				t.Fatalf("dispatch calls = %d, error = %v", calls, err)
			}
		})
	}
}
