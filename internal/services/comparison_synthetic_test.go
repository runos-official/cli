package services

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// These field types are synthetic. The served manifest 49.4.0 declares
// only array-typed nodeAffinityTags as a clearable update field.
func TestSyntheticClearableStringAndUnsupportedType(t *testing.T) {
	update := &manifest.Command{Input: &manifest.Input{Fields: []manifest.Field{
		{Name: "placementNote", Type: "string", Clearable: true},
		{Name: "capacity", Type: "integer", Clearable: true},
	}}}
	server := clearableService(map[string]any{"placementNote": "keep", "capacity": 4})
	for _, tc := range []struct {
		name, yaml string
		want       map[string]any
		refused    bool
	}{
		{"present null", "placementNote:\ncapacity: 4\n", nil, false},
		{"explicit empty", "placementNote: \"\"\ncapacity: 4\n", map[string]any{"placementNote": ""}, false},
		{"deleted string", "capacity: 4\n", map[string]any{"placementNote": ""}, false},
		{"deleted unsupported", "placementNote: keep\n", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := loadComparisonYAML(t, "postgresql", tc.yaml)
			result := CompareServiceState(local, server, update, nil)
			if !jsonEqual(result.PatchBody, tc.want) {
				t.Fatalf("patch = %#v, want %#v", result.PatchBody, tc.want)
			}
			if got := len(result.Refused) > 0; got != tc.refused {
				t.Fatalf("refused = %#v", result.Refused)
			}
			if tc.refused && !strings.Contains(result.Refused[0], "capacity") {
				t.Fatalf("refusal does not name field: %#v", result.Refused)
			}
		})
	}
}

func TestComparisonPlanJSONOmitsBareAffinity(t *testing.T) {
	m := productionClearableManifest(t)
	update, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatal(err)
	}
	local := loadComparisonYAML(t, "vllm", "nodeAffinityTags:\nreplicas: 2\n")
	server := &ServiceYAML{Type: "vllm", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{"nodeAffinityTags": []any{"gpu:shared"}, "replicas": 1}}
	plan := ComputeSyncPlan(local, server, nil, update, nil)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	patch, ok := decoded["patchBody"].(map[string]any)
	if !ok || !jsonEqual(patch, map[string]any{"replicas": float64(2)}) {
		t.Fatalf("JSON patch = %#v", patch)
	}
	if _, present := decoded["removals"]; present || strings.Contains(decoded["diff"].(string), "nodeAffinityTags") {
		t.Fatalf("JSON preview includes bare affinity: %s", raw)
	}
}
