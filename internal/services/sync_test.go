package services

import (
	"reflect"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
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
	plan := ComputeSyncPlan(local, nil, addCmd, updateCmd, nil)
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
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, nil)
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
		Type: "postgresql", ID: "svc01", CID: "mycluster2", AID: "acct1",
		Fields: map[string]any{
			"name":                       "shared-db",
			"resourceRequirementClassId": "postgresql.c0.small",
			"replicas":                   1,
			"cpuLimitMc":                 1000,
			"memoryLimitMb":              2048,
		},
	}
	server := &ServiceYAML{
		Type: "postgresql", ID: "svc01", CID: "mycluster2", AID: "acct1",
		Fields: map[string]any{
			"name":                       "shared-db",
			"resourceRequirementClassId": "postgresql.c0.beff",
			"replicas":                   1,
			"cpuLimitMc":                 1000,
			"memoryLimitMb":              2048,
		},
	}
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, nil)
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
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, nil)
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
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, nil)
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

// TestLiftServiceFlags pins the nested-flags expansion that lets pulled
// yaml round-trip through CREATE without refusal. Pull writes
// `flags: {apacheAge: true}` (mirroring the show response); the wire
// body for add expects each flag at the top level. Regression target:
// I9-I (flags half).
func TestLiftServiceFlags(t *testing.T) {
	t.Parallel()
	addCmd := &manifest.Command{
		Input: &manifest.Input{
			Flags: []manifest.Flag{
				{Name: "apacheAge"},
				{Name: "vector"},
				{Name: "documentDBExtension"},
			},
		},
	}
	addNoFlags := &manifest.Command{Input: &manifest.Input{}}

	t.Run("known flags lifted to top level", func(t *testing.T) {
		got := liftServiceFlags(map[string]any{
			"name":  "poll-app-db",
			"flags": map[string]any{"apacheAge": true, "vector": false},
		}, addCmd)
		if got["apacheAge"] != true {
			t.Errorf("apacheAge not lifted: %v", got)
		}
		if got["vector"] != false {
			t.Errorf("vector not lifted: %v", got)
		}
		if _, has := got["flags"]; has {
			t.Errorf("flags block should be empty after lift, got %v", got["flags"])
		}
		if got["name"] != "poll-app-db" {
			t.Errorf("top-level fields must be preserved: %v", got)
		}
	})

	t.Run("unknown nested flag stays under flags", func(t *testing.T) {
		got := liftServiceFlags(map[string]any{
			"flags": map[string]any{"apacheAge": true, "typoFlag": true},
		}, addCmd)
		if got["apacheAge"] != true {
			t.Errorf("known flag not lifted")
		}
		leftover, ok := got["flags"].(map[string]any)
		if !ok || leftover["typoFlag"] != true {
			t.Errorf("unknown nested flag should remain so refusedDrift surfaces it; got %v", got["flags"])
		}
	})

	t.Run("no flags key returns input unchanged", func(t *testing.T) {
		in := map[string]any{"name": "x"}
		got := liftServiceFlags(in, addCmd)
		if got["name"] != "x" {
			t.Errorf("name lost: %v", got)
		}
	})

	t.Run("addCmd has no Input.Flags: no-op", func(t *testing.T) {
		in := map[string]any{"flags": map[string]any{"apacheAge": true}}
		got := liftServiceFlags(in, addNoFlags)
		flagsBlock, ok := got["flags"].(map[string]any)
		if !ok || flagsBlock["apacheAge"] != true {
			t.Errorf("flags should stay nested when no Input.Flags declared: %v", got)
		}
	})

	t.Run("nil inputs", func(t *testing.T) {
		if got := liftServiceFlags(nil, addCmd); got != nil {
			t.Errorf("nil local should return nil, got %v", got)
		}
		if got := liftServiceFlags(map[string]any{"x": 1}, nil); len(got) != 1 || got["x"] != 1 {
			t.Errorf("nil addCmd should return local unchanged, got %v", got)
		}
	})
}

// TestCustomSynthesisHint pins the I9-H client-side warning: a sync body
// that combines a named (non-custom) resourceRequirementClassId with one
// of the class-coupled override fields (replicas / cpu* / memory*) will
// flip RRC to 'custom' server-side; emit a hint at plan time so users
// see the transformation before the next pull writes 'custom' back.
func TestCustomSynthesisHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		body      map[string]any
		serverRRC string
		want      string // empty = no hint expected
	}{
		{name: "empty body", body: nil, want: ""},
		{name: "no override in body", body: map[string]any{"name": "x"}, serverRRC: "postgresql.c0.beff", want: ""},
		{name: "RRC custom + replicas (already custom)", body: map[string]any{"resourceRequirementClassId": "custom", "replicas": 2}, want: ""},
		{name: "named class alone in body, no override", body: map[string]any{"resourceRequirementClassId": "postgresql.c0.beff"}, want: ""},
		{name: "named class in body + replicas: hint", body: map[string]any{"resourceRequirementClassId": "postgresql.c0.beff", "replicas": 2}, want: "postgresql.c0.beff"},
		{name: "named class in body + cpu request", body: map[string]any{"resourceRequirementClassId": "valkey.c0.beff", "cpuRequestMc": 250}, want: "cpuRequestMc"},
		{name: "named class + all overrides", body: map[string]any{"resourceRequirementClassId": "postgresql.c2.large", "replicas": 3, "cpuRequestMc": 500, "cpuLimitMc": 2000, "memoryRequestMb": 256, "memoryLimitMb": 1024}, want: "replicas"},
		{name: "PATCH override only, serverRRC named: hint (implicit flip)", body: map[string]any{"replicas": 3}, serverRRC: "postgresql.c0.beff", want: "postgresql.c0.beff"},
		{name: "PATCH override only, serverRRC custom: no hint", body: map[string]any{"replicas": 3}, serverRRC: "custom", want: ""},
		{name: "PATCH override only, no serverRRC (CREATE-ish): no hint", body: map[string]any{"replicas": 3}, want: ""},
		{name: "PATCH body's RRC overrides serverRRC for the gate", body: map[string]any{"resourceRequirementClassId": "custom", "replicas": 3}, serverRRC: "postgresql.c0.beff", want: ""},
		{name: "PATCH body's RRC named + override, serverRRC empty", body: map[string]any{"resourceRequirementClassId": "postgresql.c0.beff", "replicas": 2}, serverRRC: "", want: "postgresql.c0.beff"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := CustomSynthesisHint(tt.body, tt.serverRRC)
			if tt.want == "" {
				if got != "" {
					t.Errorf("expected no hint, got %q", got)
				}
				return
			}
			if got == "" {
				t.Fatalf("expected hint mentioning %q, got empty", tt.want)
			}
			if !contains(got, tt.want) {
				t.Errorf("hint %q does not contain %q", got, tt.want)
			}
		})
	}
}

// TestRefusedDriftSplitsKnownVsUnknown pins the I9-G error-wording split:
// known-but-immutable fields keep the original "requires recreation"
// wording, while unknown fields get a "typo?" hint instead of the
// misleading recreation suggestion.
func TestRefusedDriftSplitsKnownVsUnknown(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{"name": true, "replicas": true}
	known := map[string]bool{"name": true, "replicas": true, "storageMb": true, "createdAt": true}

	t.Run("known immutable on update: recreation wording", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"storageMb": 20000},
			map[string]any{"storageMb": 10000},
			allowed, false, known,
		)
		if len(out) != 1 || !contains(out[0], "requires service recreation") {
			t.Errorf("known-immutable update should say recreation, got %v", out)
		}
	})

	t.Run("unknown field on update: typo wording", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"bogusField": "anything"},
			nil,
			allowed, false, known,
		)
		if len(out) != 1 || !contains(out[0], "typo") {
			t.Errorf("unknown update field should suggest typo, got %v", out)
		}
		if contains(out[0], "requires service recreation") {
			t.Errorf("unknown field should NOT say recreation, got %v", out[0])
		}
	})

	t.Run("known on create: read-only on creation wording", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"createdAt": "2026-01-01"},
			nil,
			allowed, true, known,
		)
		if len(out) != 1 || !contains(out[0], "read-only on creation") {
			t.Errorf("known field on create should say read-only on creation, got %v", out)
		}
	})

	t.Run("unknown on create: typo wording", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"bogusField": "x"},
			nil,
			allowed, true, known,
		)
		if len(out) != 1 || !contains(out[0], "typo") {
			t.Errorf("unknown create field should suggest typo, got %v", out)
		}
	})

	t.Run("nil knownFields falls back to generic recreation wording", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"x": 1},
			nil,
			allowed, false, nil,
		)
		if len(out) != 1 || !contains(out[0], "typo") {
			t.Errorf("nil knownFields treats every refused as unknown (no show data to disambiguate), got %v", out)
		}
	})

	t.Run("matching server value: not refused", func(t *testing.T) {
		out := refusedDrift(
			map[string]any{"storageMb": 10000},
			map[string]any{"storageMb": 10000},
			allowed, false, known,
		)
		if len(out) != 0 {
			t.Errorf("matching server value shouldn't be refused, got %v", out)
		}
	})
}

// TestServicesSyncPlan_RedactSecrets pins the redaction extended to the
// services sync JSON path (I10-I parity + I10-M-style safety). Bodies
// whose keys look sensitive (password, token, ...) have their values
// masked; non-sensitive config (name, replicas, RRC enum) stays
// legible so users can verify the plan.
func TestServicesSyncPlan_RedactSecrets(t *testing.T) {
	t.Parallel()
	plan := &SyncPlan{
		CreateBody: map[string]any{
			"name":     "poll-app-db",
			"password": "p@ssw0rd",
			"apiKey":   "key-abc",
			"replicas": 1,
		},
		PatchBody: map[string]any{
			"adminToken":                 "tok-secret",
			"resourceRequirementClassId": "postgresql.c0.beff",
		},
	}
	plan.RedactSecrets()

	if plan.CreateBody["password"] != "<redacted>" {
		t.Errorf("password not redacted: %v", plan.CreateBody["password"])
	}
	if plan.CreateBody["apiKey"] != "<redacted>" {
		t.Errorf("apiKey not redacted: %v", plan.CreateBody["apiKey"])
	}
	if plan.CreateBody["name"] != "poll-app-db" {
		t.Errorf("name should stay legible, got %v", plan.CreateBody["name"])
	}
	if plan.CreateBody["replicas"] != 1 {
		t.Errorf("replicas should stay legible, got %v", plan.CreateBody["replicas"])
	}
	if plan.PatchBody["adminToken"] != "<redacted>" {
		t.Errorf("adminToken not redacted: %v", plan.PatchBody["adminToken"])
	}
	if plan.PatchBody["resourceRequirementClassId"] != "postgresql.c0.beff" {
		t.Errorf("RRC enum should stay legible, got %v", plan.PatchBody["resourceRequirementClassId"])
	}
}

func TestServicesSyncPlan_RedactSecrets_NilSafe(t *testing.T) {
	t.Parallel()
	var plan *SyncPlan
	plan.RedactSecrets()          // must not panic
	(&SyncPlan{}).RedactSecrets() // empty bodies, must not panic
}
