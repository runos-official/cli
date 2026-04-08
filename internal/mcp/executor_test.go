package mcp

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

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
