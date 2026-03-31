package mcp

import "testing"

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
