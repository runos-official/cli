package dynacmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

func TestUnflattenBody(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want map[string]any
	}{
		{
			name: "flat keys only",
			body: map[string]any{
				"name": "my-service",
				"port": 8080,
			},
			want: map[string]any{
				"name": "my-service",
				"port": 8080,
			},
		},
		{
			name: "single level dot notation",
			body: map[string]any{
				"providerConfig.location": "hel1",
				"providerConfig.type":     "cx11",
			},
			want: map[string]any{
				"providerConfig": map[string]any{
					"location": "hel1",
					"type":     "cx11",
				},
			},
		},
		{
			name: "deep nesting with three levels",
			body: map[string]any{
				"a.b.c": "deep",
			},
			want: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": "deep",
					},
				},
			},
		},
		{
			name: "mixed flat and nested",
			body: map[string]any{
				"name":                    "test",
				"providerConfig.location": "hel1",
			},
			want: map[string]any{
				"name": "test",
				"providerConfig": map[string]any{
					"location": "hel1",
				},
			},
		},
		{
			name: "empty input",
			body: map[string]any{},
			want: map[string]any{},
		},
		{
			name: "multiple siblings under same parent",
			body: map[string]any{
				"config.cpu":    2,
				"config.memory": "4Gi",
				"config.disk":   "20Gi",
			},
			want: map[string]any{
				"config": map[string]any{
					"cpu":    2,
					"memory": "4Gi",
					"disk":   "20Gi",
				},
			},
		},
		{
			name: "value types are preserved",
			body: map[string]any{
				"nested.str":  "hello",
				"nested.num":  42,
				"nested.bool": true,
				"nested.nil":  nil,
			},
			want: map[string]any{
				"nested": map[string]any{
					"str":  "hello",
					"num":  42,
					"bool": true,
					"nil":  nil,
				},
			},
		},
		{
			name: "four levels deep",
			body: map[string]any{
				"a.b.c.d": "value",
			},
			want: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": map[string]any{
							"d": "value",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unflattenBody(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unflattenBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMarkDefaultCluster(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		defaultCID string
		want       string
	}{
		{
			name:       "marks the matching cluster",
			data:       `[{"cid":"abc-123","name":"prod"},{"cid":"def-456","name":"staging"}]`,
			defaultCID: "abc-123",
			want:       `[{"cid":"abc-123*","name":"prod"},{"cid":"def-456","name":"staging"}]`,
		},
		{
			name:       "no match leaves data unchanged",
			data:       `[{"cid":"abc-123","name":"prod"}]`,
			defaultCID: "no-match",
			want:       `[{"cid":"abc-123","name":"prod"}]`,
		},
		{
			name:       "empty default CID returns data unchanged",
			data:       `[{"cid":"abc-123","name":"prod"}]`,
			defaultCID: "",
			want:       `[{"cid":"abc-123","name":"prod"}]`,
		},
		{
			name:       "invalid JSON returns data unchanged",
			data:       `this is not json`,
			defaultCID: "abc-123",
			want:       `this is not json`,
		},
		{
			name:       "empty array",
			data:       `[]`,
			defaultCID: "abc-123",
			want:       `[]`,
		},
		{
			name:       "multiple clusters with match in the middle",
			data:       `[{"cid":"a","name":"one"},{"cid":"b","name":"two"},{"cid":"c","name":"three"}]`,
			defaultCID: "b",
			want:       `[{"cid":"a","name":"one"},{"cid":"b*","name":"two"},{"cid":"c","name":"three"}]`,
		},
		{
			name:       "item without cid field is skipped",
			data:       `[{"name":"no-cid"},{"cid":"abc","name":"has-cid"}]`,
			defaultCID: "abc",
			want:       `[{"name":"no-cid"},{"cid":"abc*","name":"has-cid"}]`,
		},
		{
			name:       "cid is not a string (number) is skipped",
			data:       `[{"cid":123,"name":"numeric-cid"},{"cid":"abc","name":"string-cid"}]`,
			defaultCID: "abc",
			want:       `[{"cid":123,"name":"numeric-cid"},{"cid":"abc*","name":"string-cid"}]`,
		},
		{
			name:       "JSON object instead of array returns data unchanged",
			data:       `{"cid":"abc-123","name":"prod"}`,
			defaultCID: "abc-123",
			want:       `{"cid":"abc-123","name":"prod"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := markDefaultCluster([]byte(tt.data), tt.defaultCID)

			// For valid JSON, compare parsed structures to avoid key-ordering issues
			var gotParsed, wantParsed any
			gotIsJSON := json.Unmarshal(got, &gotParsed) == nil
			wantIsJSON := json.Unmarshal([]byte(tt.want), &wantParsed) == nil

			if gotIsJSON && wantIsJSON {
				if !reflect.DeepEqual(gotParsed, wantParsed) {
					t.Errorf("markDefaultCluster() = %s, want %s", string(got), tt.want)
				}
			} else {
				// For non-JSON (invalid input), compare raw bytes
				if string(got) != tt.want {
					t.Errorf("markDefaultCluster() = %s, want %s", string(got), tt.want)
				}
			}
		})
	}
}

func TestParseKeyValueTags(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want []map[string]string
	}{
		{
			name: "key:value pairs",
			tags: []string{"env:production", "team:backend"},
			want: []map[string]string{
				{"key": "env", "value": "production"},
				{"key": "team", "value": "backend"},
			},
		},
		{
			name: "key only without value",
			tags: []string{"important"},
			want: []map[string]string{
				{"key": "important"},
			},
		},
		{
			name: "mixed key:value and key-only",
			tags: []string{"env:prod", "critical"},
			want: []map[string]string{
				{"key": "env", "value": "prod"},
				{"key": "critical"},
			},
		},
		{
			name: "empty input",
			tags: []string{},
			want: []map[string]string{},
		},
		{
			name: "value contains colons",
			tags: []string{"url:https://example.com:8080"},
			want: []map[string]string{
				{"key": "url", "value": "https://example.com:8080"},
			},
		},
		{
			name: "single key:value pair",
			tags: []string{"name:my-app"},
			want: []map[string]string{
				{"key": "name", "value": "my-app"},
			},
		},
		{
			name: "empty value after colon",
			tags: []string{"key:"},
			want: []map[string]string{
				{"key": "key", "value": ""},
			},
		},
		{
			name: "empty key before colon",
			tags: []string{":value"},
			want: []map[string]string{
				{"key": "", "value": "value"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseKeyValueTags(tt.tags)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseKeyValueTags() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCoerceArrayFlagValue pins the I25-B contract: pflag's StringArray
// collects each --flag invocation verbatim (no CSV splitting), then the
// executor JSON-coerces the result so single-invocation JSON arrays
// land on the wire as real arrays of objects.
// TestRefuseAmbiguousKeyValueArray pins the I26-E CLI-side gate: users
// passing `--service-port-mappings 'port=3000'` (the obsolete I25-B
// workaround) now hit a pre-network error pointing at the JSON shape
// instead of letting conductor refuse with "must be an object".
func TestRefuseAmbiguousKeyValueArray(t *testing.T) {
	t.Parallel()
	t.Run("bare key=value element refused", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("service-port-mappings", []string{"port=3000"})
		if err == nil {
			t.Fatal("expected pre-network refusal")
		}
		for _, want := range []string{"--service-port-mappings", "port=3000", "JSON", "standardHttps"} {
			if !contains(err.Error(), want) {
				t.Errorf("expected %q in error, got: %s", want, err.Error())
			}
		}
	})
	t.Run("multiple comma-separated k=v elements refused", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("foo", []string{"port=3000,standardHttps=true"})
		if err == nil {
			t.Fatal("expected pre-network refusal")
		}
	})
	t.Run("JSON object element passes", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("foo", []string{`{"port":3000,"standardHttps":true}`})
		if err != nil {
			t.Errorf("JSON should pass, got: %v", err)
		}
	})
	t.Run("JSON array element passes", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("foo", []string{`[{"port":3000}]`})
		if err != nil {
			t.Errorf("JSON array should pass, got: %v", err)
		}
	})
	t.Run("bare string list with no = passes", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("tags", []string{"one", "two"})
		if err != nil {
			t.Errorf("bare strings should pass, got: %v", err)
		}
	})
	t.Run("empty slice passes", func(t *testing.T) {
		if err := refuseAmbiguousKeyValueArray("foo", nil); err != nil {
			t.Errorf("nil should pass, got: %v", err)
		}
	})
	t.Run("mixed JSON + k=v refused on the k=v element", func(t *testing.T) {
		err := refuseAmbiguousKeyValueArray("foo", []string{`{"port":3000}`, "port=9090"})
		if err == nil {
			t.Fatal("expected refusal for mixed input")
		}
		if !contains(err.Error(), "port=9090") {
			t.Errorf("error should name the offending element, got: %s", err.Error())
		}
	})
}

func TestCoerceArrayFlagValue(t *testing.T) {
	t.Parallel()
	t.Run("single JSON array unwraps", func(t *testing.T) {
		got := coerceArrayFlagValue([]string{`[{"port":3000,"standardHttps":true}]`})
		arr, ok := got.([]any)
		if !ok {
			t.Fatalf("expected []any, got %T", got)
		}
		if len(arr) != 1 {
			t.Fatalf("expected 1 element, got %d", len(arr))
		}
		m, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("expected element to be object, got %T", arr[0])
		}
		if m["port"].(float64) != 3000 {
			t.Errorf("port = %v, want 3000", m["port"])
		}
		if m["standardHttps"] != true {
			t.Errorf("standardHttps = %v, want true", m["standardHttps"])
		}
	})

	t.Run("multiple JSON objects via repeated flag", func(t *testing.T) {
		got := coerceArrayFlagValue([]string{`{"port":3000}`, `{"port":9090}`})
		arr, ok := got.([]any)
		if !ok {
			t.Fatalf("expected []any, got %T", got)
		}
		if len(arr) != 2 {
			t.Fatalf("expected 2 elements, got %d", len(arr))
		}
		first := arr[0].(map[string]any)
		if first["port"].(float64) != 3000 {
			t.Errorf("first port = %v, want 3000", first["port"])
		}
	})

	t.Run("bare string list passes through", func(t *testing.T) {
		got := coerceArrayFlagValue([]string{"one", "two"})
		strs, ok := got.([]string)
		if !ok {
			t.Fatalf("expected []string passthrough, got %T", got)
		}
		if strs[0] != "one" || strs[1] != "two" {
			t.Errorf("expected [one two], got %v", strs)
		}
	})

	t.Run("empty list passes through", func(t *testing.T) {
		got := coerceArrayFlagValue([]string{})
		strs, ok := got.([]string)
		if !ok {
			t.Fatalf("expected []string passthrough, got %T", got)
		}
		if len(strs) != 0 {
			t.Errorf("expected empty slice, got %v", strs)
		}
	})

	t.Run("mixed JSON + bare string falls back to []string", func(t *testing.T) {
		got := coerceArrayFlagValue([]string{`{"a":1}`, "bare"})
		strs, ok := got.([]string)
		if !ok {
			t.Fatalf("expected []string fallback on heterogeneous input, got %T", got)
		}
		if strs[0] != `{"a":1}` || strs[1] != "bare" {
			t.Errorf("expected verbatim passthrough, got %v", strs)
		}
	})
}

func TestFormatAuthError(t *testing.T) {
	t.Parallel()
	t.Run("non-401 returns false", func(t *testing.T) {
		_, ok := formatAuthError(&APIError{StatusCode: 500, Body: []byte(`{"error":"boom"}`)})
		if ok {
			t.Error("expected false for non-401")
		}
	})
	t.Run("non-APIError returns false", func(t *testing.T) {
		_, ok := formatAuthError(errString("transport: connection refused"))
		if ok {
			t.Error("expected false for non-APIError")
		}
	})
	t.Run("401 with bare Invalid token surfaces hints", func(t *testing.T) {
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: []byte(`{"error":"Invalid token"}`)})
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"authentication refused", "Invalid token", "RUNOS_API_KEY", "RUNOS_API_URL", "api-keys list"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("401 with reason=revoked surfaces the timestamp distinctly", func(t *testing.T) {
		body := []byte(`{"error":"Invalid token","reason":"revoked","revokedAt":"2026-05-12T10:11:12Z"}`)
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: body})
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"revoked at 2026-05-12T10:11:12Z"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("401 with reason=expired surfaces the timestamp distinctly", func(t *testing.T) {
		body := []byte(`{"error":"Invalid token","reason":"expired","expiredAt":"2026-05-12T10:11:12Z"}`)
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: body})
		if !ok {
			t.Fatal("expected ok=true on 401")
		}
		for _, want := range []string{"expired at 2026-05-12T10:11:12Z"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("401 with unparseable body still gets hints", func(t *testing.T) {
		msg, ok := formatAuthError(&APIError{StatusCode: 401, Body: []byte(`not-json`)})
		if !ok {
			t.Fatal("expected ok=true on 401 even when body is not JSON")
		}
		if !contains(msg, "unauthorized") {
			t.Errorf("expected fallback message, got: %s", msg)
		}
	})
}

func TestFormatDependentsError(t *testing.T) {
	t.Parallel()
	t.Run("non-409 returns false", func(t *testing.T) {
		_, ok := formatDependentsError(&APIError{StatusCode: 404, Body: []byte(`{"error":"nope"}`)})
		if ok {
			t.Error("expected false for non-409")
		}
	})
	t.Run("409 with no dependents returns false", func(t *testing.T) {
		body := []byte(`{"error":"some other 409"}`)
		_, ok := formatDependentsError(&APIError{StatusCode: 409, Body: body})
		if ok {
			t.Error("expected false when dependents list is missing")
		}
	})
	t.Run("409 with dependents formats nicely", func(t *testing.T) {
		body := []byte(`{
			"error": "service has dependents",
			"dependents": [
				{"type":"app","id":"abc12","name":"poll-app","alias":"poll-app-db"},
				{"type":"app","id":"def34","name":"auth-svc","alias":"auth-db"}
			]
		}`)
		msg, ok := formatDependentsError(&APIError{StatusCode: 409, Body: body})
		if !ok {
			t.Fatal("expected ok=true when dependents present")
		}
		for _, want := range []string{"service has dependents", "poll-app", "abc12", "poll-app-db", "auth-svc"} {
			if !contains(msg, want) {
				t.Errorf("expected %q in formatted message, got:\n%s", want, msg)
			}
		}
	})
	t.Run("non-APIError returns false", func(t *testing.T) {
		_, ok := formatDependentsError(errString("some other error"))
		if ok {
			t.Error("expected false for non-APIError")
		}
	})
}

type errString string

func (e errString) Error() string { return string(e) }

// I4-G regression: dynacmd's --json error path emits a structured
// envelope to stdout instead of the bare cobra "Error: ..." stderr
// surface. We capture stdout, run the helper with a typed APIError,
// and assert the envelope shape (`error`, `statusCode`).
//
// I10-G refinement: when the APIError body parses as {"error": "..."},
// the envelope's `error` field surfaces the inner message directly,
// dropping the "API error (NNN): {...}" wrapper. Pre-fix the wrapper
// stringified the inner JSON, forcing CI consumers to nested `jq`.
func TestEmitJSONErrorAndSilence_TypedAPIError(t *testing.T) {
	apiErr := &APIError{StatusCode: http.StatusNotFound, Body: []byte(`{"error":"App 'xxx' not found"}`)}

	cmd := &cobra.Command{}
	stdout, restore := captureStdout(t)
	defer restore()

	got := emitJSONErrorAndSilence(cmd, apiErr)
	captured := stdout()

	if got == nil || got.Error() != apiErr.Error() {
		t.Errorf("expected original error returned, got %v", got)
	}
	if !cmd.SilenceErrors || !cmd.SilenceUsage {
		t.Errorf("expected SilenceErrors+SilenceUsage on the cobra cmd")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(captured), &envelope); err != nil {
		t.Fatalf("stdout must be valid JSON, got %q (err: %v)", captured, err)
	}
	// I10-G: inner message surfaces directly.
	wantMsg := "App 'xxx' not found"
	if envelope["error"] != wantMsg {
		t.Errorf(`envelope["error"] = %v, want %q (I10-G: inner API body should surface, not the wrapper "API error (NNN): {...}")`, envelope["error"], wantMsg)
	}
	statusCode, ok := envelope["statusCode"].(float64)
	if !ok || int(statusCode) != http.StatusNotFound {
		t.Errorf(`envelope["statusCode"] = %v (type %T), want %d`, envelope["statusCode"], envelope["statusCode"], http.StatusNotFound)
	}
}

// I4-G partner: when the error chain is a wrapped APIError, the
// envelope still carries the typed status code so CI parsers can
// branch on it without string-matching the message.
func TestEmitJSONErrorAndSilence_WrappedAPIError(t *testing.T) {
	apiErr := &APIError{StatusCode: http.StatusForbidden, Body: []byte(`{"error":"forbidden"}`)}
	wrapped := fmt.Errorf("during fetch: %w", apiErr)

	cmd := &cobra.Command{}
	stdout, restore := captureStdout(t)
	defer restore()

	emitJSONErrorAndSilence(cmd, wrapped)
	captured := stdout()

	var envelope map[string]any
	if err := json.Unmarshal([]byte(captured), &envelope); err != nil {
		t.Fatalf("stdout must be valid JSON, got %q (err: %v)", captured, err)
	}
	if statusCode, ok := envelope["statusCode"].(float64); !ok || int(statusCode) != http.StatusForbidden {
		t.Errorf("expected wrapped APIError to surface statusCode=403, got envelope %+v", envelope)
	}
}

// I15-C: when the conductor body carries extra structured fields
// alongside `error` (e.g. `upstream: {provider, status, body}` for
// upstream-provider faults from integrations_add cloudflare/DO/etc.),
// they must flow through the --json envelope verbatim so CI consumers
// can branch on provider-side vs platform-side failures. Pre-fix the
// envelope hard-coded `{error, statusCode}` and dropped everything else.
func TestEmitJSONErrorAndSilence_ForwardsUnknownTopLevelFields(t *testing.T) {
	body := []byte(`{
		"error": "Cloudflare: 400 invalid token",
		"upstream": {
			"provider": "cloudflare",
			"status": 400,
			"body": {"errors": [{"code": 6003, "message": "Invalid format for Authorization header"}]}
		}
	}`)
	apiErr := &APIError{StatusCode: http.StatusBadGateway, Body: body}

	cmd := &cobra.Command{}
	stdout, restore := captureStdout(t)
	defer restore()

	emitJSONErrorAndSilence(cmd, apiErr)
	captured := stdout()

	var envelope map[string]any
	if err := json.Unmarshal([]byte(captured), &envelope); err != nil {
		t.Fatalf("stdout must be valid JSON, got %q (err: %v)", captured, err)
	}
	if envelope["error"] != "Cloudflare: 400 invalid token" {
		t.Errorf(`envelope["error"] = %v, want "Cloudflare: 400 invalid token"`, envelope["error"])
	}
	if statusCode, ok := envelope["statusCode"].(float64); !ok || int(statusCode) != http.StatusBadGateway {
		t.Errorf(`envelope["statusCode"] = %v, want %d`, envelope["statusCode"], http.StatusBadGateway)
	}
	upstream, ok := envelope["upstream"].(map[string]any)
	if !ok {
		t.Fatalf("envelope must carry upstream field as object, got envelope %+v", envelope)
	}
	if upstream["provider"] != "cloudflare" {
		t.Errorf(`upstream["provider"] = %v, want "cloudflare"`, upstream["provider"])
	}
	if upstream["status"].(float64) != 400 {
		t.Errorf(`upstream["status"] = %v, want 400`, upstream["status"])
	}
	if _, ok := upstream["body"].(map[string]any); !ok {
		t.Errorf("upstream.body must be a nested object (not escaped-JSON-in-string), got %T: %v", upstream["body"], upstream["body"])
	}
}

// I4-G partner: a plain (non-typed) error still produces a valid
// envelope; the statusCode field is simply absent.
func TestEmitJSONErrorAndSilence_PlainError(t *testing.T) {
	cmd := &cobra.Command{}
	stdout, restore := captureStdout(t)
	defer restore()

	emitJSONErrorAndSilence(cmd, fmt.Errorf("network unreachable"))
	captured := stdout()

	var envelope map[string]any
	if err := json.Unmarshal([]byte(captured), &envelope); err != nil {
		t.Fatalf("stdout must be valid JSON, got %q (err: %v)", captured, err)
	}
	if envelope["error"] != "network unreachable" {
		t.Errorf(`envelope["error"] = %v, want "network unreachable"`, envelope["error"])
	}
	if _, ok := envelope["statusCode"]; ok {
		t.Errorf("statusCode must be absent for non-APIError, got %+v", envelope)
	}
}

// I4-K CLI follow-up: the dynacmd dispatch must opt into the
// conductor's `?merge=true` query param specifically for `apps/update`,
// the partial-PATCH command. Without it, omitting cpu/memory or the 5
// healthCheck/metrics fields wipes them server-side. Pinned per shape
// so a future endpoint string change can't silently regress this.
func TestAppendMergeQuery(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/myacct/mycluster2/apps/abc12", "/myacct/mycluster2/apps/abc12?merge=true"},
		{"/myacct/mycluster2/apps/abc12?", "/myacct/mycluster2/apps/abc12?&merge=true"},
		{"/myacct/mycluster2/apps/abc12?foo=bar", "/myacct/mycluster2/apps/abc12?foo=bar&merge=true"},
		// Idempotent: a second call doesn't double-add.
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

// captureStdout redirects os.Stdout into a buffer for the duration of
// the returned restore func. The capture func reads-and-resets so
// callers can assert mid-test without re-doing setup.
func captureStdout(t *testing.T) (capture func() string, restore func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	capture = func() string {
		_ = w.Close()
		<-done
		// Re-open so subsequent writes still go somewhere; tests calling
		// capture once typically don't write again.
		os.Stdout = prev
		return buf.String()
	}
	restore = func() {
		os.Stdout = prev
	}
	return capture, restore
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestValidateInputValues pins the client-side validation gates for
// integer fields (non-negative) and required string fields (non-empty),
// closing I9-A (negative --tail / --since on apps_logs API 500'd) and
// I9-E client half (empty --id "" reaching the server with the literal
// :id placeholder).
func TestValidateInputValues(t *testing.T) {
	cmdLogs := manifest.Command{
		Command: "apps/logs",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "tail", Type: "integer", Default: 100},
				{Name: "since", Type: "integer"},
			},
		},
	}
	cmdRequiredFlag := manifest.Command{
		Command: "apps/foo",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "name", Type: "string", Required: true},
			},
		},
	}
	cmdClustersShow := manifest.Command{
		Command: "clusters/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "cid", Type: "string", Positional: true, Required: true},
			},
		},
	}

	cases := []struct {
		name    string
		args    []string
		cmd     manifest.Command
		body    map[string]any
		wantErr string
	}{
		{name: "negative tail (int)", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": -5}, wantErr: "--tail must be non-negative, got -5"},
		{name: "negative since (int)", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": 1, "since": -10}, wantErr: "--since must be non-negative, got -10"},
		// I27-AB: --tail 0 on pod-logs commands is refused client-side
		// because kubelet treats it as "use default" (returns ~1 entry),
		// not "no entries", which surprises every caller.
		{name: "zero tail refused on pod-logs (I27-AB)", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": 0}, wantErr: "--tail 0 is ambiguous"},
		{name: "positive tail OK", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": 100}},
		{name: "negative float64 yaml-decoded", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": float64(-3)}, wantErr: "--tail must be non-negative, got -3"},
		{name: "empty positional id (I9-E)", cmd: cmdLogs, args: []string{""}, body: map[string]any{"id": ""}, wantErr: "id is required: pass as positional <id> or --id; got empty value"},
		{name: "empty flag id no positional", cmd: cmdLogs, args: []string{}, body: map[string]any{"id": ""}, wantErr: "id is required"},
		{name: "non-empty id OK", cmd: cmdLogs, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "tail": 1}},
		{name: "empty cid skipped (handled elsewhere)", cmd: cmdClustersShow, args: []string{""}, body: map[string]any{"cid": ""}},
		{name: "empty required flag (string)", cmd: cmdRequiredFlag, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "name": ""}, wantErr: "--name is required, got empty value"},
		{name: "non-empty required flag OK", cmd: cmdRequiredFlag, args: []string{"appid1"}, body: map[string]any{"id": "appid1", "name": "x"}},
		{name: "nil input section", cmd: manifest.Command{}, args: []string{"x"}, body: nil},
		// I12-I R2: integer-typed positional flag value reaches the body
		// as an int (not a string). The empty-required gate used to only
		// check the string branch and rejected `--id 47` as empty even
		// though the body carried `id: 47`. Pin both happy + edge cases.
		{
			name: "I12-I integer positional via flag OK",
			cmd: manifest.Command{
				Command: "account/notify-keys/update",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "integer", Positional: true, Required: true},
					{Name: "name", Type: "string", Required: true},
				}},
			},
			args: []string{},
			body: map[string]any{"id": 47, "name": "x"},
		},
		{
			name: "I12-I integer positional via positional OK",
			cmd: manifest.Command{
				Command: "account/notify-keys/delete",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "integer", Positional: true, Required: true},
				}},
			},
			args: []string{"47"},
			body: map[string]any{},
		},
		{
			name: "I12-I integer positional absent fails",
			cmd: manifest.Command{
				Command: "account/notify-keys/delete",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "integer", Positional: true, Required: true},
				}},
			},
			args:    []string{},
			body:    map[string]any{},
			wantErr: "id is required",
		},
		{
			name: "I12-I boolean positional via flag OK",
			cmd: manifest.Command{
				Command: "hypothetical/toggle",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "enabled", Type: "boolean", Positional: true, Required: true},
				}},
			},
			args: []string{},
			body: map[string]any{"enabled": false},
		},
		// I13-K: AllowEmpty: true on a required string field skips the
		// empty-string refusal so `nodes/rename --name ""` (clear to
		// bootstrap default) reaches the wire.
		{
			name: "I13-K AllowEmpty required string accepts empty",
			cmd: manifest.Command{
				Command: "nodes/rename",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "nid", Type: "string", Positional: true, Required: true},
					{Name: "name", Type: "string", Required: true, AllowEmpty: true},
				}},
			},
			args: []string{"b40b4878-uuid"},
			body: map[string]any{"nid": "b40b4878-uuid", "name": ""},
		},
		// I13-K guard: AllowEmpty: false still refuses empty for
		// required string fields.
		{
			name: "I13-K guard: required string without AllowEmpty still refuses empty",
			cmd: manifest.Command{
				Command: "hypothetical/strict",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "label", Type: "string", Required: true},
				}},
			},
			args:    []string{},
			body:    map[string]any{"label": ""},
			wantErr: "--label is required, got empty value",
		},
		// I9-A mirror: AllowEmpty: true also covers positional fields
		// (in case the conductor marks the secret-files filename or
		// similar with allowEmpty). Positional empty slot ("") is
		// honoured when the field opts in.
		{
			name: "I9-A mirror: AllowEmpty: true accepts empty positional slot",
			cmd: manifest.Command{
				Command: "hypothetical/cleared-positional",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "name", Type: "string", Positional: true, Required: true, AllowEmpty: true},
				}},
			},
			args: []string{""},
			body: map[string]any{},
		},
		{
			name: "I9-A mirror: AllowEmpty: true accepts empty body value from -f file",
			cmd: manifest.Command{
				Command: "hypothetical/cleared-positional",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "name", Type: "string", Positional: true, Required: true, AllowEmpty: true},
				}},
			},
			args: []string{},
			body: map[string]any{"name": ""},
		},
		{
			name: "I9-A mirror guard: AllowEmpty: false on positional still refuses empty",
			cmd: manifest.Command{
				Command: "hypothetical/strict-positional",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "filename", Type: "string", Positional: true, Required: true},
				}},
			},
			args:    []string{""},
			body:    map[string]any{},
			wantErr: "filename is required",
		},
		// I19-G: pod-logs `--since` is capped at 90 days. Pre-fix,
		// `apps_logs --since 9999999999` (10 billion seconds = 317
		// years) returned a bare 500 from the conductor's int handling.
		{
			name:    "I19-G apps/logs --since over 90-day cap rejected",
			cmd:     cmdLogs,
			args:    []string{"appid1"},
			body:    map[string]any{"id": "appid1", "since": 9999999999},
			wantErr: "--since 9999999999 seconds exceeds the 7776000-second (90-day) cap",
		},
		{
			name: "I19-G services/<type>/logs --since over cap rejected (predicate-driven)",
			cmd: manifest.Command{
				Command: "services/postgresql/{id}/logs",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "string", Positional: true, Required: true},
					{Name: "tail", Type: "integer", Default: 100},
					{Name: "since", Type: "integer"},
				}},
			},
			args:    []string{"lw0vp"},
			body:    map[string]any{"id": "lw0vp", "since": 10000000},
			wantErr: "--since 10000000 seconds exceeds the 7776000-second (90-day) cap",
		},
		{
			name: "I19-G non-logs commands NOT capped (since field semantics differ)",
			cmd: manifest.Command{
				Command: "some/other/command",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "since", Type: "integer"},
				}},
			},
			args: []string{},
			body: map[string]any{"since": 9999999999},
		},
		{
			name:    "I19-G apps/logs --since at cap boundary OK",
			cmd:     cmdLogs,
			args:    []string{"appid1"},
			body:    map[string]any{"id": "appid1", "since": 7776000},
		},
		{
			name:    "I19-G apps/logs --since just over cap rejected",
			cmd:     cmdLogs,
			args:    []string{"appid1"},
			body:    map[string]any{"id": "appid1", "since": 7776001},
			wantErr: "exceeds the 7776000-second (90-day) cap",
		},
		{
			name:    "I19-G apps/logs --since float64 (yaml-decoded) over cap rejected",
			cmd:     cmdLogs,
			args:    []string{"appid1"},
			body:    map[string]any{"id": "appid1", "since": float64(8000000)},
			wantErr: "exceeds the 7776000-second",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInputValues(tt.args, tt.cmd, tt.body)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestNormalizeCamelToKebab pins the pflag NormalizeFunc that lets MCP
// camelCase flag spellings hit the same flag as the canonical kebab
// form. Closes I9-L: an LLM/user copying `overrideId: xxx` from MCP
// docs would otherwise hit "unknown flag --overrideId" on the CLI.
func TestNormalizeCamelToKebab(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"id", "id"},
		{"cid", "cid"},
		{"app-id", "app-id"},
		{"appId", "app-id"},
		{"overrideId", "override-id"},
		{"clusterDomainId", "cluster-domain-id"},
		{"resourceRequirementClassId", "resource-requirement-class-id"},
		{"foo", "foo"},
		// I13-G: acronym-aware kebab treats consecutive uppercase as
		// one word; "FOO" is a single all-uppercase acronym → "foo",
		// not the prior naive "f-o-o" split.
		{"FOO", "foo"},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := string(normalizeCamelToKebab(nil, tt.in))
			if got != tt.want {
				t.Errorf("normalizeCamelToKebab(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestValidatePositionalFlagAgreement pins the typo-detection guard
// added for I8-F: the same field receiving two different values via
// the positional slot AND `--<name>` flag is always a typo (since the
// two slots are interchangeable for positional fields), so refusing
// catches the mistake before dispatch. Equal values are fine.
func TestValidatePositionalFlagAgreement(t *testing.T) {
	cmdAppsShow := manifest.Command{
		Command: "apps/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
			},
		},
	}
	cmdOverrideShow := manifest.Command{
		Command: "apps/overrides/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "overrideId", Type: "string", Positional: true, Required: true},
			},
		},
	}
	cmdNoInput := manifest.Command{Command: "apps/list"}
	// I27-AC: the boolean-flag space-form footgun reproduces against
	// `apps overrides update --id ultbd --enabled false` because
	// `--enabled` is no-value boolean and `false` lands in the
	// positional slot.
	cmdOverrideUpdate := manifest.Command{
		Command: "apps/overrides/update",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
			},
			Flags: []manifest.Flag{
				{Name: "enabled"},
			},
		},
	}

	cases := []struct {
		name    string
		args    []string
		cmd     manifest.Command
		body    map[string]any
		wantErr string
	}{
		{
			name: "positional only, no flag",
			args: []string{"appid1"},
			cmd:  cmdAppsShow,
			body: map[string]any{},
		},
		{
			name: "flag only, no positional",
			args: nil,
			cmd:  cmdAppsShow,
			body: map[string]any{"id": "appid1"},
		},
		{
			name: "both match",
			args: []string{"appid1"},
			cmd:  cmdAppsShow,
			body: map[string]any{"id": "appid1"},
		},
		{
			name:    "both disagree (I8-F repro)",
			args:    []string{"appid1"},
			cmd:     cmdAppsShow,
			body:    map[string]any{"id": "zzzzz"},
			wantErr: `ambiguous id: positional "appid1" and --id="zzzzz" disagree`,
		},
		{
			name: "second positional disagrees on overrideId",
			args: []string{"appid1", "AAA"},
			cmd:  cmdOverrideShow,
			body: map[string]any{"overrideId": "BBB"},
			wantErr: `ambiguous overrideId: positional "AAA" and --override-id="BBB" disagree`,
		},
		{
			name: "first positional disagrees on id",
			args: []string{"appid1", "AAA"},
			cmd:  cmdOverrideShow,
			body: map[string]any{"id": "zzzzz"},
			wantErr: `ambiguous id: positional "appid1" and --id="zzzzz" disagree`,
		},
		{
			name: "flag empty string skipped (not a real conflict)",
			args: []string{"appid1"},
			cmd:  cmdAppsShow,
			body: map[string]any{"id": ""},
		},
		{
			name: "no input section",
			args: []string{"appid1"},
			cmd:  cmdNoInput,
			body: map[string]any{"id": "zzzzz"},
		},
		{
			// I27-AC: boolean flag space-form (`--enabled false`) lands
			// the literal "false" in the next positional slot; the
			// disagreement error gains a pointer at `--<flag>=false`
			// (equals-form) so the user isn't chasing the wrong end.
			name: "bool flag space-form lands false in positional (I27-AC)",
			args: []string{"false"},
			cmd:  cmdOverrideUpdate,
			body: map[string]any{"id": "ultbd", "enabled": true},
			wantErr: `If you meant a boolean flag, use --<flag>=false`,
		},
		{
			// I27-AC partner: same as above but the literal positional
			// is "true" (matching `--enabled true` cobra-parses where
			// --enabled is already true-by-presence).
			name: "bool flag space-form lands true in positional (I27-AC)",
			args: []string{"true"},
			cmd:  cmdOverrideUpdate,
			body: map[string]any{"id": "ultbd"},
			wantErr: `If you meant a boolean flag, use --<flag>=true`,
		},
		{
			name: "positional absent but flag set is fine",
			args: nil,
			cmd:  cmdOverrideShow,
			body: map[string]any{"id": "appid1"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePositionalFlagAgreement(tt.args, tt.cmd, tt.body)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestBuildQueryParams pins the GET/DELETE query-string assembly that
// turns a manifest's Input definition + resolved body into URL params.
// Specifically targets I7-F: pre-fix the GET branch in buildEndpoint
// only iterated Input.Fields and silently dropped Input.Flags, so
// `apps_logs --previous=true` arrived as `?tail=N` and the conductor's
// `if (previous)` gate never tripped. The helper is now symmetric for
// GET + DELETE (positional Fields skipped, every non-positional Field
// included, every Flag included).
func TestBuildQueryParams(t *testing.T) {
	cmdLogs := manifest.Command{
		Command: "apps/logs",
		Method:  "GET",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "tail", Type: "integer", Default: 100},
				{Name: "since", Type: "integer"},
			},
			Flags: []manifest.Flag{
				{Name: "previous", Default: false},
			},
		},
	}
	cmdDelete := manifest.Command{
		Command: "apps/delete",
		Method:  "DELETE",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
			},
			Flags: []manifest.Flag{
				{Name: "force", Default: false},
			},
		},
	}
	cmdNoInput := manifest.Command{Command: "apps/list", Method: "GET"}
	cmdFieldsOnly := manifest.Command{
		Command: "apps/show",
		Method:  "GET",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
			},
		},
	}

	t.Run("GET drops positional, keeps fields and flag (I7-F repro)", func(t *testing.T) {
		got := buildQueryParams(cmdLogs, map[string]any{
			"id":       "appid1",
			"tail":     50,
			"previous": true,
		})
		if got.Get("id") != "" {
			t.Errorf("positional id should not appear in query string, got %q", got.Get("id"))
		}
		if got.Get("tail") != "50" {
			t.Errorf("tail: got %q, want 50", got.Get("tail"))
		}
		if got.Get("previous") != "true" {
			t.Errorf("previous flag missing from GET query: got %q, want true (I7-F)", got.Get("previous"))
		}
	})

	t.Run("GET with flag default false still emits", func(t *testing.T) {
		got := buildQueryParams(cmdLogs, map[string]any{
			"tail":     100,
			"previous": false,
		})
		if got.Get("previous") != "false" {
			t.Errorf("previous=false should serialise: got %q", got.Get("previous"))
		}
	})

	t.Run("DELETE preserves prior symmetric behavior", func(t *testing.T) {
		got := buildQueryParams(cmdDelete, map[string]any{
			"id":    "appid1",
			"force": true,
		})
		if got.Get("id") != "" {
			t.Errorf("positional id leaked into DELETE query: %q", got.Get("id"))
		}
		if got.Get("force") != "true" {
			t.Errorf("force flag missing from DELETE query: %q", got.Get("force"))
		}
	})

	t.Run("body missing key skips the param", func(t *testing.T) {
		got := buildQueryParams(cmdLogs, map[string]any{"tail": 10})
		if got.Get("since") != "" {
			t.Errorf("since should be absent when not in body, got %q", got.Get("since"))
		}
		if got.Get("previous") != "" {
			t.Errorf("previous should be absent when not in body, got %q", got.Get("previous"))
		}
	})

	t.Run("nil Input returns empty", func(t *testing.T) {
		got := buildQueryParams(cmdNoInput, map[string]any{"anything": "ignored"})
		if len(got) != 0 {
			t.Errorf("nil Input should return empty params, got %v", got)
		}
	})

	t.Run("no flags declared", func(t *testing.T) {
		got := buildQueryParams(cmdFieldsOnly, map[string]any{})
		if len(got) != 0 {
			t.Errorf("no body entries, no flags: empty; got %v", got)
		}
	})

	t.Run("encoded URL string is stable", func(t *testing.T) {
		// Note: queryParams.Encode() sorts keys, so the order is deterministic.
		got := buildQueryParams(cmdLogs, map[string]any{
			"tail":     100,
			"previous": true,
		})
		encoded := got.Encode()
		want := "previous=true&tail=100"
		if encoded != want {
			t.Errorf("Encode(): got %q, want %q", encoded, want)
		}
	})
}

// TestPositionalArgForField pins the slot lookup that wires positional
// args to named manifest fields. Specifically targets the `cid` slot
// on commands like `clusters/show` so a typo positional doesn't
// silently fall through to the default cluster (regression I7-C/I7-D).
func TestPositionalArgForField(t *testing.T) {
	cmdClusters := manifest.Command{
		Command: "clusters/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "cid", Type: "string", Positional: true, Required: true},
			},
		},
	}
	cmdAppsShow := manifest.Command{
		Command: "apps/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
			},
		},
	}
	cmdOverrideShow := manifest.Command{
		Command: "apps/overrides/show",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "overrideId", Type: "string", Positional: true, Required: true},
			},
		},
	}
	cmdMixed := manifest.Command{
		Command: "apps/secret-files/update",
		Input: &manifest.Input{
			Fields: []manifest.Field{
				{Name: "id", Type: "string", Positional: true, Required: true},
				{Name: "add", Type: "array"},
				{Name: "remove", Type: "array"},
			},
		},
	}
	cmdNoInput := manifest.Command{Command: "apps/list"}

	cases := []struct {
		name string
		args []string
		cmd  manifest.Command
		want string
		want_field string
	}{
		{name: "cid present", args: []string{"notreal"}, cmd: cmdClusters, want: "notreal", want_field: "cid"},
		{name: "cid absent (no args)", args: nil, cmd: cmdClusters, want: "", want_field: "cid"},
		{name: "wrong field name", args: []string{"x"}, cmd: cmdClusters, want: "", want_field: "id"},
		{name: "apps/show id slot", args: []string{"appid1"}, cmd: cmdAppsShow, want: "appid1", want_field: "id"},
		{name: "apps/show cid not positional", args: []string{"appid1"}, cmd: cmdAppsShow, want: "", want_field: "cid"},
		{name: "overrides id slot 0", args: []string{"appid1", "Sv1"}, cmd: cmdOverrideShow, want: "appid1", want_field: "id"},
		{name: "overrides overrideId slot 1", args: []string{"appid1", "Sv1"}, cmd: cmdOverrideShow, want: "Sv1", want_field: "overrideId"},
		{name: "mixed only counts positional", args: []string{"appid1"}, cmd: cmdMixed, want: "appid1", want_field: "id"},
		{name: "mixed non-positional skipped", args: []string{"appid1"}, cmd: cmdMixed, want: "", want_field: "add"},
		{name: "no input section", args: []string{"x"}, cmd: cmdNoInput, want: "", want_field: "cid"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := positionalArgForField(tt.args, tt.cmd, tt.want_field); got != tt.want {
				t.Errorf("positionalArgForField(%v, %s, %q) = %q, want %q", tt.args, tt.cmd.Command, tt.want_field, got, tt.want)
			}
		})
	}
}

// TestBodyFileProvidesField pins the third-source check for required
// positional fields: `-f body.yaml` with `id: x` satisfies the
// missing-arg check in builder.RunE the same way the positional slot or
// `--id` would. Regression target for I6-D.
func TestBodyFileProvidesField(t *testing.T) {
	dir := t.TempDir()

	writeFile := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	withID := writeFile("with-id.yaml", "id: abc12\nmountPath: /etc/secrets\n")
	emptyID := writeFile("empty-id.yaml", "id: \"\"\nmountPath: /etc\n")
	noID := writeFile("no-id.yaml", "mountPath: /etc/secrets\n")
	nullID := writeFile("null-id.yaml", "id: null\nmountPath: /etc\n")
	intID := writeFile("int-id.yaml", "id: 42\n")
	garbage := writeFile("garbage.yaml", "::not yaml::\n")

	cases := []struct {
		name      string
		path      string
		field     string
		want      bool
	}{
		{"empty path", "", "id", false},
		{"empty field", withID, "", false},
		{"missing file", filepath.Join(dir, "nope.yaml"), "id", false},
		{"field present non-empty string", withID, "id", true},
		{"field present empty string", emptyID, "id", false},
		{"field absent", noID, "id", false},
		{"field null", nullID, "id", false},
		{"field present int", intID, "id", true},
		{"garbage yaml", garbage, "id", false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := bodyFileProvidesField(tt.path, tt.field); got != tt.want {
				t.Errorf("bodyFileProvidesField(%q, %q) = %v, want %v", tt.path, tt.field, got, tt.want)
			}
		})
	}
}

// TestBodyFilePresentsField pins the I13-K sibling helper: returns true
// for any key-present case (including null and empty-string), so
// AllowEmpty-flagged fields satisfy the missing-required gate when the
// user supplies an explicit empty value via `-f`. Stricter contract
// than bodyFileProvidesField, kept as a separate helper so existing
// callers don't silently widen.
func TestBodyFilePresentsField(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	withName := writeFile("with-name.yaml", "name: my-node\n")
	emptyName := writeFile("empty-name.yaml", "name: \"\"\n")
	nullName := writeFile("null-name.yaml", "name: null\n")
	noName := writeFile("no-name.yaml", "id: abc\n")

	cases := []struct {
		name  string
		path  string
		field string
		want  bool
	}{
		{"empty path", "", "name", false},
		{"empty field", withName, "", false},
		{"missing file", filepath.Join(dir, "nope.yaml"), "name", false},
		{"field present non-empty", withName, "name", true},
		{"field present empty string", emptyName, "name", true},
		{"field present null", nullName, "name", true},
		{"field absent", noName, "name", false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := bodyFilePresentsField(tt.path, tt.field); got != tt.want {
				t.Errorf("bodyFilePresentsField(%q, %q) = %v, want %v", tt.path, tt.field, got, tt.want)
			}
		})
	}
}

// TestAPIErrorFromBody pins the defensive 2xx-with-error-envelope check
// that turns `{"error":"...","statusCode":4xx}` into an *APIError so
// downstream callers see a non-nil error and the process exits non-zero
// (I11-Q: `apps builds --id <deleted-app>` exits 0 on 404).
func TestAPIErrorFromBody(t *testing.T) {
	cases := []struct {
		name       string
		httpStatus int
		body       string
		wantErr    bool
		wantCode   int
	}{
		{"plain success body", 200, `[{"jobId":"abc"}]`, false, 0},
		{"empty success body", 200, ``, false, 0},
		{"4xx pass-through skipped", 404, `{"error":"x","statusCode":404}`, false, 0},
		{"envelope without statusCode", 200, `{"error":"some text"}`, false, 0},
		{"envelope without error", 200, `{"statusCode":404}`, false, 0},
		{"envelope with 2xx statusCode", 200, `{"error":"x","statusCode":200}`, false, 0},
		{"envelope with empty error", 200, `{"error":"","statusCode":404}`, false, 0},
		{"valid envelope 404", 200, `{"error":"App 'kb2gc' not found","statusCode":404}`, true, 404},
		{"valid envelope 500", 200, `{"error":"boom","statusCode":500}`, true, 500},
		{"garbage body", 200, `not json at all`, false, 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := apiErrorFromBody(tt.httpStatus, []byte(tt.body))
			if tt.wantErr {
				if got == nil {
					t.Fatalf("apiErrorFromBody(%d, %q) = nil, want *APIError", tt.httpStatus, tt.body)
				}
				if got.StatusCode != tt.wantCode {
					t.Errorf("StatusCode = %d, want %d", got.StatusCode, tt.wantCode)
				}
				return
			}
			if got != nil {
				t.Errorf("apiErrorFromBody(%d, %q) = %+v, want nil", tt.httpStatus, tt.body, got)
			}
		})
	}
}

// TestDomainCheckExitGate pins the exit-code gate that flips
// `tools/domain-check` to non-zero when the result is anything other
// than a healthy match. Two ways to fail: matchStatus != "healthy"
// (I11-J R1), or matched=false on a response that has no matchStatus
// field (I11-J R2: the not-matched DNS-doesn't-resolve shape).
func TestDomainCheckExitGate(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"healthy passes", `{"matched":true,"matchStatus":"healthy"}`, false},
		{"degraded fails", `{"matched":true,"matchStatus":"degraded","matchStatusDescription":"DNS resolves but expected cid differs"}`, true},
		{"not-matched explicit status fails", `{"matched":false,"matchStatus":"not-matched"}`, true},
		// I11-J R2: not-matched shape has no matchStatus, just matched=false.
		{"matched false without matchStatus fails", `{"matched":false,"resolvedIps":[]}`, true},
		{"matched true without matchStatus passes (defensive)", `{"matched":true}`, false},
		{"healthy with matched=true passes", `{"matched":true,"matchStatus":"healthy","resolvedIps":["1.2.3.4"]}`, false},
		{"empty body passes (defensive)", `{}`, false},
		{"garbage body passes (defensive)", `not json`, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := domainCheckExitGate([]byte(tt.body))
			if tt.wantErr && err == nil {
				t.Errorf("domainCheckExitGate(%q) = nil, want non-nil error", tt.body)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("domainCheckExitGate(%q) = %v, want nil", tt.body, err)
			}
		})
	}
}

// TestExtractLogsDiagnostic pins the synthesised-diagnostic recogniser
// for `apps logs --previous`. The conductor mixes a single diagnostic
// entry into the log array when no previous container exists; the CLI
// lifts it into a top-level envelope or stderr notice (I11-S).
func TestExtractLogsDiagnostic(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantMsg  string
		wantHit  bool
	}{
		{
			name:    "single diagnostic entry",
			body:    `[{"containerName":"<diagnostic>","message":"No previous container instance available","podName":"pod-1","timestamp":"2026-05-11T00:00:00Z"}]`,
			wantMsg: "No previous container instance available",
			wantHit: true,
		},
		{
			name:    "real log entry (no diagnostic marker)",
			body:    `[{"containerName":"app","message":"hello","podName":"pod-1","timestamp":"2026-05-11T00:00:00Z"}]`,
			wantHit: false,
		},
		{
			name:    "diagnostic mixed with real logs",
			body:    `[{"containerName":"<diagnostic>","message":"x"},{"containerName":"app","message":"y"}]`,
			wantHit: false,
		},
		{"empty array", `[]`, "", false},
		{"empty body", ``, "", false},
		{"object body", `{}`, "", false},
		{"diagnostic with empty message", `[{"containerName":"<diagnostic>","message":""}]`, "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotMsg, gotHit := extractLogsDiagnostic([]byte(tt.body))
			if gotHit != tt.wantHit {
				t.Fatalf("hit = %v, want %v (msg=%q)", gotHit, tt.wantHit, gotMsg)
			}
			if tt.wantHit && gotMsg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", gotMsg, tt.wantMsg)
			}
		})
	}
}

// TestUidProvided pins the I12-C R2 helper that gates the
// user/permissions JWT auto-fill. Auto-fill must skip injection when
// the user has already supplied a value via either the positional
// slot OR the explicit --uid flag; otherwise the flag value clobbers
// the positional and the I8-F ambiguity guard fires when the two
// disagree (e.g. positional = docId, auto-filled flag = firebase uid).
func TestUidProvided(t *testing.T) {
	cmd := manifest.Command{
		Command: "user/permissions",
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "uid", Type: "string", Positional: true, Required: true},
		}},
	}

	cases := []struct {
		name string
		args []string
		body map[string]any
		want bool
	}{
		{"positional provided", []string{"docId-xyz"}, map[string]any{}, true},
		{"flag provided", []string{}, map[string]any{"uid": "firebase-abc"}, true},
		{"both provided", []string{"docId-xyz"}, map[string]any{"uid": "firebase-abc"}, true},
		{"neither provided", []string{}, map[string]any{}, false},
		{"empty positional", []string{""}, map[string]any{}, false},
		{"empty flag", []string{}, map[string]any{"uid": ""}, false},
		{"non-string body value treated as absent", []string{}, map[string]any{"uid": 123}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := uidProvided(tt.args, cmd, tt.body, "uid"); got != tt.want {
				t.Errorf("uidProvided(%v, %v) = %v, want %v", tt.args, tt.body, got, tt.want)
			}
		})
	}

	t.Run("nil input section returns false", func(t *testing.T) {
		if got := uidProvided([]string{"x"}, manifest.Command{}, map[string]any{}, "uid"); got != false {
			t.Errorf("expected false for nil input, got %v", got)
		}
	})
}

// TestParseDurationOrInt pins the I14-F widening: --since accepts both
// integer seconds ("300") and Go duration strings ("5m", "1h30m"),
// converting to int seconds for the wire body. Negative values are
// refused regardless of shape; malformed strings produce a typed error
// that names both accepted shapes so the up-stack `--<flag>:` prefix
// gives a complete diagnostic.
func TestParseDurationOrInt(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"integer seconds", "300", 300, false},
		{"zero seconds", "0", 0, false},
		{"duration 5m", "5m", 300, false},
		{"duration 1h", "1h", 3600, false},
		{"duration 1h30m", "1h30m", 5400, false},
		{"duration 90s", "90s", 90, false},
		{"sub-second rounds down", "500ms", 0, false},
		{"empty", "", 0, true},
		{"negative int", "-5", 0, true},
		{"negative duration", "-5m", 0, true},
		{"garbage", "5banana", 0, true},
		{"bare unit", "m", 0, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDurationOrInt(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseDurationOrInt(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDurationOrInt(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseDurationOrInt(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestFilterUnseenLogEntries pins the I14-E follow-loop dedup: poll
// responses overlap by design (the second poll's window includes
// some entries from the first), so the seen-set keys on the four
// fields the conductor populates per entry. The first call seeds;
// subsequent calls return only entries with new keys.
func TestFilterUnseenLogEntries(t *testing.T) {
	seen := make(map[string]struct{})

	first := []byte(`[
		{"timestamp":"2026-05-12T10:00:00Z","podName":"p1","containerName":"app","message":"hello"},
		{"timestamp":"2026-05-12T10:00:01Z","podName":"p1","containerName":"app","message":"world"}
	]`)
	gotFirst := filterUnseenLogEntries(first, seen)
	if len(gotFirst) != 2 {
		t.Fatalf("first call: got %d entries, want 2", len(gotFirst))
	}

	// Second poll: window overlaps first, returns line 2 again plus new line 3.
	second := []byte(`[
		{"timestamp":"2026-05-12T10:00:01Z","podName":"p1","containerName":"app","message":"world"},
		{"timestamp":"2026-05-12T10:00:02Z","podName":"p1","containerName":"app","message":"new"}
	]`)
	gotSecond := filterUnseenLogEntries(second, seen)
	if len(gotSecond) != 1 {
		t.Fatalf("second call: got %d entries, want 1 (only the new one)", len(gotSecond))
	}
	if msg, _ := gotSecond[0]["message"].(string); msg != "new" {
		t.Errorf("expected message=%q, got %q", "new", msg)
	}

	// Third poll: nothing new.
	third := []byte(`[
		{"timestamp":"2026-05-12T10:00:02Z","podName":"p1","containerName":"app","message":"new"}
	]`)
	gotThird := filterUnseenLogEntries(third, seen)
	if len(gotThird) != 0 {
		t.Errorf("third call: got %d entries, want 0", len(gotThird))
	}

	// Identical content from a different pod is a new key (pod is part
	// of the identity), so the row should emit.
	fourth := []byte(`[
		{"timestamp":"2026-05-12T10:00:00Z","podName":"p2","containerName":"app","message":"hello"}
	]`)
	gotFourth := filterUnseenLogEntries(fourth, seen)
	if len(gotFourth) != 1 {
		t.Errorf("fourth call: same message on a different pod should emit; got %d entries", len(gotFourth))
	}

	// Malformed body returns empty without panicking.
	if got := filterUnseenLogEntries([]byte(`not-json`), seen); len(got) != 0 {
		t.Errorf("malformed body: got %v, want empty", got)
	}
}

// resetStdinYAMLCache clears the package-level cache so each sub-test
// starts clean. Tests can't run in parallel against `-` stdin since
// os.Stdin and the sync.Once are global, so we just gate sequentially.
func resetStdinYAMLCache(t *testing.T) {
	t.Helper()
	stdinYAMLOnce = sync.Once{}
	stdinYAMLData = nil
	stdinYAMLErr = nil
}

// TestLoadYAMLFileStdin pins the I15-B fix: `-f -` reads from stdin
// once and caches the parsed body so PreRunE required-field checks
// and the RunE collectInput pass both see the same content.
func TestLoadYAMLFileStdin(t *testing.T) {
	t.Run("reads YAML from stdin", func(t *testing.T) {
		resetStdinYAMLCache(t)
		origStdin := os.Stdin
		t.Cleanup(func() { os.Stdin = origStdin; resetStdinYAMLCache(t) })

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdin = r
		if _, err := w.Write([]byte("name: foo\nvalue: bar\n")); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
		_ = w.Close()

		got, err := loadYAMLFile("-")
		if err != nil {
			t.Fatalf("loadYAMLFile: %v", err)
		}
		if got["name"] != "foo" || got["value"] != "bar" {
			t.Errorf("got %+v, want name=foo value=bar", got)
		}
	})

	t.Run("repeated reads return cached body (sync.Once)", func(t *testing.T) {
		resetStdinYAMLCache(t)
		origStdin := os.Stdin
		t.Cleanup(func() { os.Stdin = origStdin; resetStdinYAMLCache(t) })

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdin = r
		if _, err := w.Write([]byte("k: v\n")); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
		_ = w.Close()

		first, err := loadYAMLFile("-")
		if err != nil {
			t.Fatalf("first load: %v", err)
		}
		second, err := loadYAMLFile("-")
		if err != nil {
			t.Fatalf("second load: %v", err)
		}
		if first["k"] != "v" || second["k"] != "v" {
			t.Errorf("expected both calls to return cached k=v, got first=%v second=%v", first, second)
		}
	})

	t.Run("malformed stdin surfaces typed parse error", func(t *testing.T) {
		resetStdinYAMLCache(t)
		origStdin := os.Stdin
		t.Cleanup(func() { os.Stdin = origStdin; resetStdinYAMLCache(t) })

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		os.Stdin = r
		if _, err := w.Write([]byte("not: : valid : yaml")); err != nil {
			t.Fatalf("write pipe: %v", err)
		}
		_ = w.Close()

		_, err = loadYAMLFile("-")
		if err == nil {
			t.Fatal("expected parse error, got nil")
		}
		if !contains(err.Error(), "parse stdin as YAML") {
			t.Errorf("error %q missing 'parse stdin as YAML' prefix", err.Error())
		}
	})

	t.Run("file path still reads from disk", func(t *testing.T) {
		resetStdinYAMLCache(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "body.yaml")
		if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		got, err := loadYAMLFile(path)
		if err != nil {
			t.Fatalf("loadYAMLFile: %v", err)
		}
		if got["a"] != 1 {
			t.Errorf("got %v, want a=1", got)
		}
	})
}

