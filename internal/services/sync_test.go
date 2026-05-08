package services

import (
	"reflect"
	"testing"
)

func TestComputeSyncPlan_Create(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	local := &ServiceYAML{
		Type: "postgresql",
		ID:   "", // empty -> create
		CID:  "mycluster3",
		AID:  "acct1",
		Fields: map[string]any{
			"name":                       "poll-app-db",
			"resourceRequirementClassId": "postgresql.c0.beff",
		},
	}
	plan := ComputeSyncPlan(local, nil, addCmd, updateCmd)
	if !plan.HasChanges() {
		t.Fatal("expected create plan to have changes")
	}
	if plan.PatchBody != nil {
		t.Errorf("expected no patch body for create, got %v", plan.PatchBody)
	}
	if plan.CreateBody == nil {
		t.Fatal("expected create body")
	}
	if plan.CreateBody["name"] != "poll-app-db" {
		t.Errorf("expected name in create body, got %v", plan.CreateBody)
	}
}

func TestComputeSyncPlan_PatchDrift(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	local := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{
			"name":     "poll-app-db",
			"replicas": 3,
		},
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{
			"name":     "poll-app-db",
			"replicas": 1,
		},
	}
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd)
	if !plan.HasChanges() {
		t.Fatal("expected drift to produce a plan")
	}
	if plan.PatchBody == nil {
		t.Fatal("expected patch body")
	}
	// PATCH body is drift-only: name matches server, only replicas drifts.
	want := map[string]any{
		"replicas": 3,
	}
	if !reflect.DeepEqual(plan.PatchBody, want) {
		t.Errorf("patch body: got %v, want %v", plan.PatchBody, want)
	}
}

// Regression test for the services_sync class-only swap bug. Pulling a
// service materializes the active class's cpu/memory baseline into the
// local yaml. If the user then edits only the class line and syncs, the
// PATCH body must contain ONLY the class change, not the OLD class's
// cpu/memory values, because resolveRRC interprets those as overrides
// against the new baseline and flips class to "custom".
func TestComputeSyncPlan_ClassOnlySwap_OmitsUnchangedResources(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	local := &ServiceYAML{
		Type: "postgresql", ID: "ecqtl", CID: "mycluster2", AID: "acct1",
		Fields: map[string]any{
			"name":                       "shared-db",
			"resourceRequirementClassId": "postgresql.c0.small",
			"replicas":                   1,
			"cpuLimitMc":                 1000,
			"memoryLimitMb":              2048,
		},
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "ecqtl", CID: "mycluster2", AID: "acct1",
		Fields: map[string]any{
			"name":                       "shared-db",
			"resourceRequirementClassId": "postgresql.c0.beff",
			"replicas":                   1,
			"cpuLimitMc":                 1000,
			"memoryLimitMb":              2048,
		},
	}
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd)
	if !plan.HasChanges() {
		t.Fatal("expected class drift to produce a plan")
	}
	if plan.PatchBody == nil {
		t.Fatal("expected patch body")
	}
	// Only the class change should be in the wire body.
	if _, ok := plan.PatchBody["cpuLimitMc"]; ok {
		t.Errorf("patch body should not include unchanged cpuLimitMc, got %v", plan.PatchBody)
	}
	if _, ok := plan.PatchBody["memoryLimitMb"]; ok {
		t.Errorf("patch body should not include unchanged memoryLimitMb, got %v", plan.PatchBody)
	}
	if _, ok := plan.PatchBody["replicas"]; ok {
		t.Errorf("patch body should not include unchanged replicas, got %v", plan.PatchBody)
	}
	if got := plan.PatchBody["resourceRequirementClassId"]; got != "postgresql.c0.small" {
		t.Errorf("patch body resourceRequirementClassId: got %v, want postgresql.c0.small", got)
	}
}

func TestComputeSyncPlan_NoDrift(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	local := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x", "replicas": 1},
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x", "replicas": 1},
	}
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd)
	if plan.HasChanges() {
		t.Errorf("identical local+server should produce no changes, got %v", plan)
	}
}

func TestComputeSyncPlan_RefusedNonPatchableField(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	// The fake manifest's update endpoint accepts name/replicas/version
	// but not storageMb. A local edit to storageMb (which would be an
	// immutable-after-create field on a real mysql) should be refused
	// since it can't be patched.
	local := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x", "replicas": 1, "storageMb": 20000},
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "abc12", CID: "mycluster3", AID: "acct1",
		Fields: map[string]any{"name": "x", "replicas": 1, "storageMb": 10000},
	}
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd)
	if len(plan.Refused) == 0 {
		t.Fatal("expected storageMb drift to surface as refused")
	}
	found := false
	for _, r := range plan.Refused {
		if containsAll(r, "storageMb") {
			found = true
		}
	}
	if !found {
		t.Errorf("refused list should mention 'storageMb', got %v", plan.Refused)
	}
	// And the patch body should NOT contain version (we don't try to send it).
	if _, ok := plan.PatchBody["version"]; ok {
		t.Errorf("patch body should not contain non-patchable version field, got %v", plan.PatchBody)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
