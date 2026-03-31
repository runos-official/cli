package jobs

import (
	"encoding/json"
	"testing"
)

func TestJobStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "completed is terminal", status: "completed", want: true},
		{name: "failed is terminal", status: "failed", want: true},
		{name: "pending is not terminal", status: "pending", want: false},
		{name: "in_progress is not terminal", status: "in_progress", want: false},
		{name: "running is not terminal", status: "running", want: false},
		{name: "cancelled is not terminal", status: "cancelled", want: false},
		{name: "empty string is not terminal", status: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &JobStatus{Status: tt.status}
			if got := j.IsTerminal(); got != tt.want {
				t.Errorf("IsTerminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkItem_Result(t *testing.T) {
	tests := []struct {
		name      string
		rawResult json.RawMessage
		want      string
	}{
		{
			name:      "nil RawResult returns empty string",
			rawResult: nil,
			want:      "",
		},
		{
			name:      "empty RawResult returns empty string",
			rawResult: json.RawMessage{},
			want:      "",
		},
		{
			name:      "string value is unwrapped",
			rawResult: json.RawMessage(`"hello world"`),
			want:      "hello world",
		},
		{
			name:      "empty string value",
			rawResult: json.RawMessage(`""`),
			want:      "",
		},
		{
			name:      "JSON object returned as raw JSON",
			rawResult: json.RawMessage(`{"key":"value"}`),
			want:      `{"key":"value"}`,
		},
		{
			name:      "JSON array returned as raw JSON",
			rawResult: json.RawMessage(`[1,2,3]`),
			want:      `[1,2,3]`,
		},
		{
			name:      "numeric value returned as raw JSON",
			rawResult: json.RawMessage(`42`),
			want:      "42",
		},
		{
			name:      "boolean value returned as raw JSON",
			rawResult: json.RawMessage(`true`),
			want:      "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WorkItem{RawResult: tt.rawResult}
			if got := w.Result(); got != tt.want {
				t.Errorf("Result() = %q, want %q", got, tt.want)
			}
		})
	}
}
