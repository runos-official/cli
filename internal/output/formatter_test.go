package output

import "testing"

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
