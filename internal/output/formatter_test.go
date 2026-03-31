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
