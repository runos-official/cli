package output

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// I24-D regression: when the manifest declared output fields but the
// live response carries MORE top-level keys (canonical case: conductor
// 14.2.0 added a `gitlabRunner` block to the gitlab-runner status
// endpoint without a CLI release), the new keys must surface in text
// mode instead of being silently dropped.
func TestFormatObject_ForwardCompatAppendsUnknownFields(t *testing.T) {
	body := []byte(`{
		"activated": true,
		"status": "active",
		"runnerId": 53134687,
		"gitlabRunner": {
			"online": true,
			"contactedAt": "2026-05-13T01:00:00Z",
			"tagList": ["runos", "mycluster2"]
		},
		"surpriseField": "future-extension"
	}`)
	f := NewFormatter(false)
	def := &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "activated"},
		{Name: "status"},
		{Name: "runnerId"},
	}}
	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	// Declared fields render in declared order (key may have trailing
	// padding before the colon for column alignment, so match the
	// `<key>` substring rather than `<key>:`).
	for _, want := range []string{"activated", "status", "runnerId"} {
		if !strings.Contains(out, want) {
			t.Errorf("declared field %q missing from output:\n%s", want, out)
		}
	}
	// Unknown top-level fields surface too (forward-compat).
	for _, want := range []string{"gitlabRunner", "surpriseField", "online", "contactedAt", "tagList"} {
		if !strings.Contains(out, want) {
			t.Errorf("forward-compat field %q missing from output:\n%s", want, out)
		}
	}
	// gitlabRunner rendered as nested block, not single-line mash.
	if strings.Contains(out, "gitlabRunner: {") || strings.Contains(out, "gitlabRunner: map[") {
		t.Errorf("nested map rendered as single-line mash:\n%s", out)
	}
}

// captureStdout swaps os.Stdout for a pipe, runs fn, restores stdout,
// and returns whatever fn printed. Helper for testing print-based
// formatters without refactoring them to take an io.Writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	<-done
	os.Stdout = orig
	return buf.String()
}

func TestGetNestedValue(t *testing.T) {
	tests := []struct {
		name  string
		item  map[string]any
		field string
		want  any
	}{
		{
			name:  "simple top-level key",
			item:  map[string]any{"name": "test"},
			field: "name",
			want:  "test",
		},
		{
			name: "dot-notation nested key",
			item: map[string]any{
				"flags": map[string]any{
					"systemInstance": true,
				},
			},
			field: "flags.systemInstance",
			want:  true,
		},
		{
			name:  "missing key returns nil",
			item:  map[string]any{"name": "test"},
			field: "missing",
			want:  nil,
		},
		{
			name: "deeply nested key",
			item: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": "deep",
					},
				},
			},
			field: "a.b.c",
			want:  "deep",
		},
		{
			name:  "nested key with non-map intermediate returns nil",
			item:  map[string]any{"a": "string_value"},
			field: "a.b",
			want:  nil,
		},
		{
			name: "partial nested path missing returns nil",
			item: map[string]any{
				"a": map[string]any{
					"b": "hello",
				},
			},
			field: "a.x",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getNestedValue(tt.item, tt.field)
			if got != tt.want {
				t.Errorf("getNestedValue() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

// I5-B regression: legacy manifest output field name `__docId` must
// resolve to the response body's `id` key when the conductor has
// normalised the field name (iter-1 11b). Pre-fix the table renderer
// looked up `__docId`, found nothing, and printed a blank column
// under the literal `__DOCID` header.
func TestGetNestedValue_LegacyDocIDAlias(t *testing.T) {
	item := map[string]any{"id": "abc12", "name": "override-1"}
	if got := getNestedValue(item, "__docId"); got != "abc12" {
		t.Errorf(`getNestedValue("__docId") = %v, want "abc12" (alias to id)`, got)
	}
	// `id` itself still resolves directly.
	if got := getNestedValue(item, "id"); got != "abc12" {
		t.Errorf(`getNestedValue("id") = %v, want "abc12"`, got)
	}
}

// I5-B partner: alias only kicks in when the manifest field name is
// the legacy one AND the response lacks it. A response that genuinely
// has `__docId` returns that value verbatim (defensive: older
// conductors still emit `__docId`).
func TestGetNestedValue_LegacyDocIDPresentInResponse(t *testing.T) {
	item := map[string]any{"__docId": "legacy", "id": "abc12"}
	if got := getNestedValue(item, "__docId"); got != "legacy" {
		t.Errorf(`getNestedValue("__docId") = %v, want "legacy" (no alias when key exists)`, got)
	}
}

// I5-B regression: column-header label upper-cases the aliased name
// for legacy fields so the table reads `ID`, not the raw Firestore
// subdoc convention `__DOCID`.
func TestHeaderLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"name", "NAME"},
		{"createdAt", "CREATEDAT"},
		{"id", "ID"},
		{"__docId", "ID"},     // alias → upper(id)
		{"enabled", "ENABLED"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := headerLabel(c.in); got != c.want {
				t.Errorf("headerLabel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{
			name:  "string value",
			input: "hello",
			want:  "hello",
		},
		{
			name:  "float64 integer renders without decimal",
			input: float64(42),
			want:  "42",
		},
		{
			name:  "float64 decimal renders with decimal",
			input: float64(3.14),
			want:  "3.14",
		},
		{
			name:  "bool true",
			input: true,
			want:  "true",
		},
		{
			name:  "bool false",
			input: false,
			want:  "false",
		},
		{
			name:  "nil returns empty string",
			input: nil,
			want:  "",
		},
		{
			name:  "simple slice joined with commas",
			input: []any{"a", "b", "c"},
			want:  "a, b, c",
		},
		{
			name:  "empty slice returns empty string",
			input: []any{},
			want:  "",
		},
		{
			name:  "nested object with state key",
			input: map[string]any{"state": "running"},
			want:  "running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.input)
			if got != tt.want {
				t.Errorf("formatValue(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestUnwrapArrayEnvelope pins the I26-U follow-up: list-style
// endpoints wrapped in single-key envelope objects unwrap cleanly so
// the text-mode formatter renders rows instead of dumping raw JSON.
func TestUnwrapArrayEnvelope(t *testing.T) {
	t.Run("envelope unwraps to inner array", func(t *testing.T) {
		got := string(unwrapArrayEnvelope([]byte(`{"apps":[{"id":"a"}]}`)))
		if got != `[{"id":"a"}]` {
			t.Errorf("got %q", got)
		}
	})
	t.Run("bare array unchanged", func(t *testing.T) {
		in := `[{"id":"a"}]`
		if string(unwrapArrayEnvelope([]byte(in))) != in {
			t.Errorf("bare array modified")
		}
	})
	t.Run("multi-key object unchanged", func(t *testing.T) {
		in := `{"apps":[],"total":0}`
		if string(unwrapArrayEnvelope([]byte(in))) != in {
			t.Errorf("multi-key object modified")
		}
	})
	t.Run("single-key object with non-array value unchanged", func(t *testing.T) {
		in := `{"app":{"id":"a"}}`
		if string(unwrapArrayEnvelope([]byte(in))) != in {
			t.Errorf("single-key non-array modified")
		}
	})
	t.Run("malformed JSON unchanged", func(t *testing.T) {
		in := `not json`
		if string(unwrapArrayEnvelope([]byte(in))) != in {
			t.Errorf("malformed input modified")
		}
	})
	t.Run("empty input unchanged", func(t *testing.T) {
		if string(unwrapArrayEnvelope([]byte(``))) != `` {
			t.Errorf("empty input modified")
		}
	})
	t.Run("whitespace tolerated", func(t *testing.T) {
		got := string(unwrapArrayEnvelope([]byte(`  { "jobs" : [ 1 , 2 ] }  `)))
		if got != `[ 1 , 2 ]` {
			t.Errorf("got %q", got)
		}
	})
	// I27-T: Conductor 17.7.0 finished the envelope-everywhere migration
	// by wrapping domains / cluster-domains / integrations / nodes /
	// services_<type>_list responses. The helper is shape-keyed not
	// key-keyed, so it works for every new envelope key without a CLI
	// release; this sub-test pins that promise to the specific keys the
	// I26-U follow-up shipped.
	t.Run("iter-27 envelope keys all unwrap", func(t *testing.T) {
		for _, key := range []string{"domains", "clusterDomains", "integrations", "nodes", "services"} {
			in := `{"` + key + `":[{"id":"x"}]}`
			got := string(unwrapArrayEnvelope([]byte(in)))
			if got != `[{"id":"x"}]` {
				t.Errorf("envelope key %q: got %q", key, got)
			}
		}
	})
	// I27-AA: Conductor 17.10.0 wrapped the three remaining bare-array
	// readers the I27-T sweep missed: services_dependents,
	// apps/:id/dependencies, services/:type/:id/dependencies. The CLI's
	// shape-keyed unwrapper already handles them; this sub-test pins
	// the closure.
	t.Run("iter-27 R2 dependents / dependencies envelopes unwrap", func(t *testing.T) {
		for _, key := range []string{"dependents", "dependencies"} {
			in := `{"` + key + `":[{"alias":"shared-cache","id":"fjd9r"}]}`
			got := string(unwrapArrayEnvelope([]byte(in)))
			if got != `[{"alias":"shared-cache","id":"fjd9r"}]` {
				t.Errorf("envelope key %q: got %q", key, got)
			}
		}
	})
}
