package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceDiffIgnoresBareAffinity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.yaml")
	content := "type: postgresql\nid: abc12\ncid: cluster1\naid: acct1\nnodeAffinityTags:\nreplicas: 1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &ServiceYAML{Type: "postgresql", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{
		"nodeAffinityTags": []any{"gpu:shared"}, "replicas": 1,
	}}
	local, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	update, err := UpdateCommand(productionClearableManifest(t), "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	diff := ComputeSemanticDiff(path, local, server, update, nil)
	if diff.Status != StatusInSync || strings.Contains(diff.UnifiedDiff, "nodeAffinityTags") {
		t.Fatalf("bare affinity creates drift: %#v", diff)
	}
}

func TestSemanticServiceDiffMatchesUpdateComparison(t *testing.T) {
	m := productionClearableManifest(t)
	forms := []string{"nodeAffinityTags:\n", "nodeAffinityTags: null\n", "nodeAffinityTags: Null\n", "nodeAffinityTags: NULL\n", "nodeAffinityTags: ~\n", "nodeAffinityTags: !!null null\n", "nodeAffinityTags: !<tag:yaml.org,2002:null> null\n"}
	for _, serviceType := range []string{"postgresql", "valkey", "mysql", "vllm", "umami"} {
		update, err := UpdateCommand(m, serviceType)
		if err != nil {
			t.Fatal(err)
		}
		if field := ClearableFields(update)["nodeAffinityTags"]; field.Type != "array" {
			t.Fatalf("%s affinity is not a clearable array: %#v", serviceType, field)
		}
		field, oldValue, newYAML := "replicas", any(1), "replicas: 2\n"
		if serviceType == "umami" {
			field, oldValue, newYAML = "name", "old-name", "name: new-name\n"
		}
		server := &ServiceYAML{Type: serviceType, ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{field: oldValue, "nodeAffinityTags": []any{"gpu:shared"}}}
		for _, form := range forms {
			for _, edit := range []bool{false, true} {
				name := serviceType + "/" + strings.TrimSpace(form)
				if edit {
					name += "/edit"
				}
				t.Run(name, func(t *testing.T) {
					fields := form
					if edit {
						fields += newYAML
					} else if field == "replicas" {
						fields += "replicas: 1\n"
					} else {
						fields += "name: old-name\n"
					}
					local := loadComparisonYAML(t, serviceType, fields)
					comparison := CompareServiceState(local, server, update, nil)
					diff := ComputeSemanticDiff("service.yaml", local, server, update, nil)
					if (diff.Status == StatusDrift) != comparison.HasDrift || diff.UnifiedDiff != comparison.Diff {
						t.Fatalf("diff and sync disagree: diff=%#v comparison=%#v", diff, comparison)
					}
					want := StatusInSync
					if edit {
						want = StatusDrift
					}
					if diff.Status != want || strings.Contains(diff.UnifiedDiff, "nodeAffinityTags") {
						t.Fatalf("bare affinity result: %#v", diff)
					}
					if edit && !strings.Contains(diff.UnifiedDiff, field) {
						t.Fatalf("edit absent from preview: %#v", diff)
					}
				})
			}
		}
	}
}

func TestSemanticServiceDiffVisibleChanges(t *testing.T) {
	m := productionClearableManifest(t)
	update, err := UpdateCommand(m, "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	server := &ServiceYAML{Type: "postgresql", ID: "abc12", CID: "cluster1", AID: "acct1", Fields: map[string]any{
		"name": "old-name", "replicas": float64(1), "nodeAffinityTags": []any{"gpu:shared"}, "storageMb": float64(100),
	}}
	for _, tc := range []struct {
		name, fields, visible string
		status                DiffStatus
	}{
		{"comment and key order", "# note\nreplicas: 1\nstorageMb: 100\nname: old-name\nnodeAffinityTags: [gpu:shared]\n", "", StatusInSync},
		{"equivalent numeric", "replicas: 1.0\nstorageMb: 100\nname: old-name\nnodeAffinityTags: [gpu:shared]\n", "", StatusInSync},
		{"quoted numeric", "replicas: '1'\nstorageMb: 100\nname: old-name\nnodeAffinityTags: [gpu:shared]\n", "replicas", StatusDrift},
		{"explicit empty", "replicas: 1\nstorageMb: 100\nname: old-name\nnodeAffinityTags: []\n", "nodeAffinityTags", StatusDrift},
		{"deleted affinity", "replicas: 1\nstorageMb: 100\nname: old-name\n", "nodeAffinityTags", StatusDrift},
		{"invalid quoted null", "replicas: 1\nstorageMb: 100\nname: old-name\nnodeAffinityTags: 'null'\n", "nodeAffinityTags", StatusDrift},
		{"invalid empty string", "replicas: 1\nstorageMb: 100\nname: old-name\nnodeAffinityTags: ''\n", "nodeAffinityTags", StatusDrift},
		{"unknown", "replicas: 1\nstorageMb: 100\nname: old-name\nnodeAffinityTags: [gpu:shared]\nunknownField: x\n", "unknownField", StatusDrift},
		{"immutable", "replicas: 1\nstorageMb: 200\nname: old-name\nnodeAffinityTags: [gpu:shared]\n", "storageMb", StatusDrift},
		{"ordinary", "replicas: 1\nstorageMb: 100\nname: new-name\nnodeAffinityTags: [gpu:shared]\n", "name", StatusDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := loadComparisonYAML(t, "postgresql", tc.fields)
			got := ComputeSemanticDiff("service.yaml", local, server, update, nil)
			if got.Status != tc.status || (tc.visible != "" && !strings.Contains(got.UnifiedDiff, tc.visible)) {
				t.Fatalf("diff = %#v, want %s with %q", got, tc.status, tc.visible)
			}
			if tc.status == StatusInSync && got.UnifiedDiff != "" {
				t.Fatalf("in_sync has a preview: %#v", got)
			}
		})
	}
	server.Fields["nodeAffinityTags"] = []any{}
	local := loadComparisonYAML(t, "postgresql", "replicas: 1\nstorageMb: 100\nname: old-name\n")
	if got := ComputeSemanticDiff("service.yaml", local, server, update, nil); got.Status != StatusInSync {
		t.Fatalf("empty server pin invents removal: %#v", got)
	}
}
