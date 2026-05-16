package cmd

import (
	"encoding/json"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Regression for issue 39: `manifest list <no-match> --json` printed
// `null` instead of `[]`. The non-empty path emitted a JSON array, so
// downstream jq pipelines (and LLM consumers reasoning about the
// introspection surface) silently broke on the mismatch. The fix seeds
// the slice as non-nil; this test pins both shapes and the JSON
// encoding contract.
func TestFilterManifestCommandPaths(t *testing.T) {
	cmds := []manifest.Command{
		{Command: "services/postgresql/add"},
		{Command: "services/valkey/add"},
		{Command: "apps/list"},
	}
	cases := []struct {
		name   string
		filter string
		want   []string
	}{
		{
			name:   "matches multiple",
			filter: "services",
			want:   []string{"services/postgresql/add", "services/valkey/add"},
		},
		{
			name:   "case-insensitive",
			filter: "SERVICES",
			want:   []string{"services/postgresql/add", "services/valkey/add"},
		},
		{
			name:   "no match",
			filter: "zzzznotreal",
			want:   []string{},
		},
		{
			name:   "empty filter matches everything",
			filter: "",
			want:   []string{"services/postgresql/add", "services/valkey/add", "apps/list"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterManifestCommandPaths(cmds, tc.filter)
			if got == nil {
				t.Fatalf("returned a nil slice; want non-nil (JSON would marshal nil to `null`)")
			}
			if !equalManifestPathList(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The user-visible contract is `runos manifest list <filter> --json`
// returning a JSON array even when nothing matches. Marshalling the
// helper's no-match output must produce `[]`, never `null`.
func TestFilterManifestCommandPaths_NoMatchMarshalsAsArray(t *testing.T) {
	got := filterManifestCommandPaths(nil, "anything")
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal returned err=%v", err)
	}
	if string(b) != "[]" {
		t.Errorf("got %s, want []", string(b))
	}
}

func equalManifestPathList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
