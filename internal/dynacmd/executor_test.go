package dynacmd

import (
	"encoding/json"
	"reflect"
	"testing"
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
