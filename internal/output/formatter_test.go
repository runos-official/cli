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

// Regression: `runos jobs show <id>` rendered blank `createdAt :` and
// `error :` rows when the API response omitted those keys, while --json
// (correctly) omitted them entirely. The text formatter now skips
// declared top-level fields that aren't present in the response so the
// two surfaces report the same field set.
func TestFormatObject_SkipsDeclaredTopLevelFieldsAbsentFromResponse(t *testing.T) {
	body := []byte(`{
		"id": "19ebbdcd-e8a1-437c-8a62-363a5ac81dbc",
		"name": "deploy.cli",
		"type": "deploy",
		"status": "succeeded",
		"progress": 100,
		"currentStep": "rollout"
	}`)
	f := NewFormatter(false)
	def := &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "id"},
		{Name: "name"},
		{Name: "type"},
		{Name: "status"},
		{Name: "progress"},
		{Name: "currentStep"},
		{Name: "error"},
		{Name: "createdAt"},
	}}
	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	for _, want := range []string{"id", "name", "type", "status", "progress", "currentStep"} {
		if !strings.Contains(out, want) {
			t.Errorf("present field %q missing from output:\n%s", want, out)
		}
	}
	for _, absent := range []string{"error", "createdAt"} {
		if strings.Contains(out, absent) {
			t.Errorf("absent field %q should be skipped but appears in output:\n%s", absent, out)
		}
	}
}

// Guard: a top-level field that IS present (even with a falsy/empty
// value) should still render so the user sees the value the API
// returned. The skip rule keys on key presence, not value emptiness.
func TestFormatObject_PresentFieldWithEmptyStringValueStillRenders(t *testing.T) {
	body := []byte(`{
		"id": "abc",
		"error": ""
	}`)
	f := NewFormatter(false)
	def := &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "id"},
		{Name: "error"},
	}}
	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	if !strings.Contains(out, "error") {
		t.Errorf("error key present with empty value should still render:\n%s", out)
	}
}

// Regression: pre-fix, formatNestedObject's default branch iterated
// `for k := range obj` which the Go spec leaves non-deterministic.
// `agents list` rendered different field orders on consecutive runs
// (`online=true, version=0.14.8, updateAvailable=false` vs.
// `version=0.14.8, updateAvailable=false, online=true`), breaking
// screenshot/diff comparisons. The default branch now sorts keys to
// match JSON's alphabetical contract.
func TestFormatNestedObject_DeterministicKeyOrder(t *testing.T) {
	obj := map[string]any{
		"online":          true,
		"version":         "0.14.8",
		"updateAvailable": false,
		"createdAt":       "2026-05-13T01:00:00Z",
	}
	first := formatNestedObject(obj, 0)
	// Repeat with the same input. A non-deterministic iteration would
	// flip on at least one of these iterations in practice; with the
	// fix every call returns the same string.
	for i := 0; i < 200; i++ {
		got := formatNestedObject(obj, 0)
		if got != first {
			t.Fatalf("non-deterministic output on iteration %d:\n  first: %s\n  got:   %s", i, first, got)
		}
	}
	// Alphabetical order: createdAt, online, updateAvailable, version.
	wantSubstrings := []string{
		"createdAt=",
		"online=true",
		"updateAvailable=false",
		"version=0.14.8",
	}
	for i, sub := range wantSubstrings {
		idx := strings.Index(first, sub)
		if idx < 0 {
			t.Errorf("output %q missing %q", first, sub)
			continue
		}
		if i > 0 {
			prev := strings.Index(first, wantSubstrings[i-1])
			if idx < prev {
				t.Errorf("output %q has %q before %q (want alphabetical)", first, sub, wantSubstrings[i-1])
			}
		}
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

// Regression test for the `runos apps list` empty PORT column bug.
//
// The apps_list / apps_show manifest entries declare a flat `port`
// field, but conductor's response carries `servicePortMappings: [{port,
// ...}]` instead, so a direct `port` lookup returned nil and the PORT
// column rendered blank for every row. The formatter now derives the
// printable value from the array when the flat lookup misses.
func TestDerivePortFromServicePortMappings(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want any
	}{
		{
			name: "missing servicePortMappings key returns nil so unrelated commands stay blank",
			in:   map[string]any{"id": "x"},
			want: nil,
		},
		{
			name: "empty array returns nil (no usable port)",
			in:   map[string]any{"servicePortMappings": []any{}},
			want: nil,
		},
		{
			name: "single mapping renders as bare port string",
			in: map[string]any{"servicePortMappings": []any{
				map[string]any{"port": float64(3000), "standardHttps": true},
			}},
			want: "3000",
		},
		{
			name: "multiple mappings comma-join in declared order",
			in: map[string]any{"servicePortMappings": []any{
				map[string]any{"port": float64(3000)},
				map[string]any{"port": float64(8080)},
			}},
			want: "3000,8080",
		},
		{
			name: "entries without a port key are skipped",
			in: map[string]any{"servicePortMappings": []any{
				map[string]any{"standardHttps": true},
				map[string]any{"port": float64(9090)},
			}},
			want: "9090",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := derivePortFromServicePortMappings(tc.in)
			if got != tc.want {
				t.Errorf("derivePortFromServicePortMappings = %v, want %v", got, tc.want)
			}
		})
	}
}

// Integration with getNestedValue: a `port` lookup that misses the flat
// key but hits the servicePortMappings fallback returns the derived
// value, while a `port` lookup on an item that has neither returns nil.
func TestGetNestedValue_PortFallsBackToServicePortMappings(t *testing.T) {
	item := map[string]any{
		"id":   "d2eow",
		"name": "iter14-app",
		"servicePortMappings": []any{
			map[string]any{"port": float64(3000), "standardHttps": true},
		},
	}
	if got := getNestedValue(item, "port"); got != "3000" {
		t.Errorf(`getNestedValue("port") = %v, want "3000" (derived from servicePortMappings)`, got)
	}
	// When a flat `port` key is present, it wins; the fallback is only
	// consulted on a nil result.
	itemWithFlatPort := map[string]any{"port": float64(8080), "servicePortMappings": []any{
		map[string]any{"port": float64(3000)},
	}}
	if got := getNestedValue(itemWithFlatPort, "port"); got != float64(8080) {
		t.Errorf(`flat "port" should win over fallback, got %v`, got)
	}
}

// Regression test for the long-NAME column blow-out bug: a single
// ~250-char value (api-key NAME, in the wild) pushed every following
// column hundreds of chars right. truncateCell caps at 40 visible
// runes in text mode; --json keeps the full value (asserted in the
// formatter integration test below).
func TestTruncateCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short string passes through", "abc", 40, "abc"},
		{"exact-length passes through", "12345", 5, "12345"},
		{"one over caps with ellipsis", "123456", 5, "12..."},
		{"long ascii truncated", strings.Repeat("a", 250), 40, strings.Repeat("a", 37) + "..."},
		{"empty string is empty", "", 40, ""},
		{"max<=3 is a no-op (no room for ellipsis)", "abcdef", 3, "abcdef"},
		// Multi-byte runes: cap by runes, not bytes, so the truncation
		// boundary doesn't split a character mid-codepoint.
		{"multi-byte rune string truncates by rune count", strings.Repeat("é", 50), 10, strings.Repeat("é", 7) + "..."},
		// Issue 106: URL-shaped values bypass the cap because the URL
		// is typically the primary value the user came for (apps
		// network-access endpoint, services dependencies targetIngressUrl)
		// and copy-paste from the terminal needs the full string.
		{"https URL bypasses cap", "https://app-c479n-3000.testing.mercatura.example.com/healthz", 40, "https://app-c479n-3000.testing.mercatura.example.com/healthz"},
		{"http URL bypasses cap", "http://internal.example.com/very/long/path/that/exceeds/40/chars", 40, "http://internal.example.com/very/long/path/that/exceeds/40/chars"},
		// Strings that merely contain http:// somewhere are NOT bypassed
		// — only http(s)://-prefixed values get the exemption.
		{"url-substring still truncated", "click here: https://example.com plus more text to overflow", 40, "click here: https://example.com plus ..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateCell(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncateCell(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// Integration check on formatArray: a long NAME value in one row must
// not push subsequent columns out of alignment. After truncation every
// row's NAME column is exactly maxTextCellWidth wide, so column N
// starts at the same x-offset on every row.
func TestFormatArray_TruncatesOversizedCellsAndPreservesAlignment(t *testing.T) {
	body := []byte(`[
		{"id":"a1","name":"` + strings.Repeat("x", 250) + `","status":"active"},
		{"id":"b2","name":"short-name","status":"revoked"}
	]`)
	f := NewFormatter(false)
	def := &manifest.Output{Type: "array", Fields: []manifest.OutputField{
		{Name: "id"}, {Name: "name"}, {Name: "status"},
	}}
	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	if strings.Contains(out, strings.Repeat("x", 250)) {
		t.Errorf("expected NAME truncation; full 250-char value still present in text output")
	}
	if !strings.Contains(out, strings.Repeat("x", 37)+"...") {
		t.Errorf("expected truncated NAME ending in `...`; got:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected header + separator + 2 rows, got %d lines:\n%s", len(lines), out)
	}
	statusCol := strings.Index(lines[0], "STATUS")
	if statusCol < 0 {
		t.Fatalf("STATUS column missing from header: %q", lines[0])
	}
	for _, line := range lines[2:] {
		if !strings.HasPrefix(line[statusCol:], "active") && !strings.HasPrefix(line[statusCol:], "revoked") {
			t.Errorf("row %q: STATUS not at column %d", line, statusCol)
		}
	}
}

// --json output bypasses the truncation path so machine consumers get
// the full untruncated value. Asserts the contract called out in the
// fix: text mode truncates, JSON preserves.
func TestFormat_JSONPreservesFullValueWhenTextWouldTruncate(t *testing.T) {
	body := []byte(`[{"id":"a1","name":"` + strings.Repeat("x", 250) + `"}]`)
	f := NewFormatter(true)
	def := &manifest.Output{Type: "array", Fields: []manifest.OutputField{
		{Name: "id"}, {Name: "name"},
	}}
	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	if !strings.Contains(out, strings.Repeat("x", 250)) {
		t.Errorf("expected full 250-char NAME in --json output; got truncated")
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
		{"__docId", "ID"}, // alias → upper(id)
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

// Regression for issue 38: `services postgresql users <id>` returns a
// two-key envelope (`{users:[...], orphanSecretsDetected:[]}`) so the
// shape-keyed single-key unwrap can't pick the primary array. The
// formatter's fallback printed the raw minified JSON to the terminal.
// pickArrayFromMultiKeyEnvelope uses the manifest's declared fields to
// pick the array whose elements carry at least one declared field name.
func TestPickArrayFromMultiKeyEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		fields []string
		want   string
	}{
		{
			name:   "postgresql users two-key envelope picks users array",
			body:   `{"users":[{"username":"u","databases":["d"]}],"orphanSecretsDetected":[]}`,
			fields: []string{"username", "databases"},
			want:   `[{"username":"u","databases":["d"]}]`,
		},
		{
			name:   "field match wins over array length",
			body:   `{"users":[{"username":"u"}],"orphanSecretsDetected":[{"name":"a"},{"name":"b"},{"name":"c"}]}`,
			fields: []string{"username", "databases"},
			want:   `[{"username":"u"}]`,
		},
		{
			name:   "empty arrays fall back to lexicographic order",
			body:   `{"users":[],"orphanSecretsDetected":[]}`,
			fields: []string{"username", "databases"},
			want:   `[]`,
		},
		{
			name:   "single-key envelope is left for unwrapArrayEnvelope",
			body:   `{"users":[{"username":"u"}]}`,
			fields: []string{"username", "databases"},
			want:   `{"users":[{"username":"u"}]}`,
		},
		{
			name:   "no fields hint returns input unchanged",
			body:   `{"users":[{"username":"u"}],"orphanSecretsDetected":[]}`,
			fields: nil,
			want:   `{"users":[{"username":"u"}],"orphanSecretsDetected":[]}`,
		},
		{
			name:   "bare array returns unchanged",
			body:   `[{"username":"u"}]`,
			fields: []string{"username"},
			want:   `[{"username":"u"}]`,
		},
		{
			name:   "no top-level array fields returns unchanged",
			body:   `{"meta":{"count":1},"version":2}`,
			fields: []string{"username"},
			want:   `{"meta":{"count":1},"version":2}`,
		},
		{
			name:   "dotted manifest fields match top-level segment",
			body:   `{"items":[{"flags":{"systemInstance":true}}],"orphans":[]}`,
			fields: []string{"flags.systemInstance"},
			want:   `[{"flags":{"systemInstance":true}}]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(pickArrayFromMultiKeyEnvelope([]byte(tc.body), tc.fields))
			if got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// End-to-end through the formatter: the postgresql users response renders
// as a tabular text view (header + row), not raw JSON. This is the user-
// visible promise from the bug report.
func TestFormatArray_PostgresUsersMultiKeyEnvelope(t *testing.T) {
	body := []byte(`{"users":[{"username":"harbor_kmb6g","databases":["harbor_kmb6g"]}],"orphanSecretsDetected":[]}`)
	def := &manifest.Output{Type: "array", Fields: []manifest.OutputField{
		{Name: "username"},
		{Name: "databases"},
	}}

	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = stdout })

	if err := NewFormatter(false).Format(body, def); err != nil {
		w.Close()
		t.Fatalf("Format returned err=%v", err)
	}
	w.Close()

	var buf bytes.Buffer
	io.Copy(&buf, r)
	got := buf.String()

	if strings.Contains(got, "orphanSecretsDetected") {
		t.Errorf("formatter leaked raw JSON envelope; got:\n%s", got)
	}
	if !strings.Contains(got, "USERNAME") {
		t.Errorf("expected table header USERNAME, got:\n%s", got)
	}
	if !strings.Contains(got, "harbor_kmb6g") {
		t.Errorf("expected row containing harbor_kmb6g, got:\n%s", got)
	}
}

// TestEmptyArrayRendersAsZeroEntries is the goal-19 A17 regression. An
// empty array rendered as a blank cell, which reads as "the server did
// not answer this field" rather than "there are none". A non-empty array
// of objects already renders as "N entries" plus a sub-table, so the
// empty case was the only one that said nothing.
func TestEmptyArrayRendersAsZeroEntries(t *testing.T) {
	f := NewFormatter(false)
	def := &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "vmid"}, {Name: "gpus"}, {Name: "disks"},
	}}
	body := []byte(`{"vmid":"vm-1","gpus":[],"disks":[{"name":"root","sizeGi":20}]}`)

	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	if !strings.Contains(out, "gpus") || !strings.Contains(out, "0 entries") {
		t.Errorf("expected `gpus: 0 entries`, got:\n%s", out)
	}
	if !strings.Contains(out, "1 entry") {
		t.Errorf("expected the non-empty array to keep its `1 entry` summary, got:\n%s", out)
	}
}

// TestNestedSubTableExpandsStructuredCells is the goal-19 A18
// regression. `vm-usage` renders per-VM rows in a sub-table, and each row
// carries `segments` and `shapeSeconds` arrays of objects. A table cell
// cannot hold a nested table, so those collapsed to `[1 entry]` and the
// only numbers the report exists to deliver were unreachable without
// --json. Rows carrying nested structure now render as blocks instead of
// table rows.
func TestNestedSubTableExpandsStructuredCells(t *testing.T) {
	f := NewFormatter(false)
	def := &manifest.Output{Type: "object", Fields: []manifest.OutputField{{Name: "vms"}}}
	body := []byte(`{"vms":[{"vmid":"vm-1","name":"web","shapeSeconds":[{"cpu":2,"seconds":3600,"state":"running"}]}]}`)

	out := captureStdout(t, func() {
		if err := f.Format(body, def); err != nil {
			t.Fatalf("format: %v", err)
		}
	})
	if strings.Contains(out, "[1 entry]") {
		t.Errorf("nested structure must not collapse to `[1 entry]`, got:\n%s", out)
	}
	for _, want := range []string{"vm-1", "3600", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the rendered report, got:\n%s", want, out)
		}
	}
}
