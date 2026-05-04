package services

import (
	"reflect"
	"testing"
)

func TestBuildPulledService_StripsServerOnlyFields(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")

	// Server response carries the union of settable + audit/computed
	// fields. BuildPulledService should keep only the settable ones
	// (per add+update Input.Fields) and drop everything else, matching
	// the "yaml is desired state" rule.
	raw := map[string]any{
		"id":                         "abc12",
		"name":                       "poll-app-db",
		"resourceRequirementClassId": "postgresql.c0.beff",
		"version":                    "17.6",
		"replicas":                   1,
		// Server-only / audit / derived: must be filtered out.
		"createdAt":        "2026-04-30T10:57:12.569Z",
		"updatedAt":        "2026-04-30T10:57:12.569Z",
		"osid":             "postgresql-abc12",
		"internalEndpoint": "postgresql-abc12-rw.svc.cluster.local:5432",
		"cpuLimitMc":       1000,
		"memoryLimitMb":    2048,
		"_slow":            []any{"status", "pods"},
	}
	got := BuildPulledService(raw, "postgresql", "mycluster3", "acct1", "abc12", addCmd, updateCmd)

	if got.Type != "postgresql" || got.ID != "abc12" || got.CID != "mycluster3" || got.AID != "acct1" {
		t.Errorf("header mismatch: %+v", got)
	}
	for _, banned := range []string{"id", "type", "cid", "aid", "createdAt", "updatedAt", "osid", "internalEndpoint", "cpuLimitMc", "memoryLimitMb", "_slow"} {
		if _, ok := got.Fields[banned]; ok {
			t.Errorf("field %q must be filtered out, got %v", banned, got.Fields[banned])
		}
	}
	want := map[string]any{
		"name":                       "poll-app-db",
		"resourceRequirementClassId": "postgresql.c0.beff",
		"version":                    "17.6",
		"replicas":                   1,
	}
	if !reflect.DeepEqual(got.Fields, want) {
		t.Errorf("Fields mismatch:\n got %v\nwant %v", got.Fields, want)
	}
}

func TestBuildPulledService_FallbackIDWhenServerOmits(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")
	raw := map[string]any{"name": "x"}
	got := BuildPulledService(raw, "postgresql", "mycluster3", "acct1", "fallback-sid", addCmd, updateCmd)
	if got.ID != "fallback-sid" {
		t.Errorf("expected fallback id, got %q", got.ID)
	}
}

func TestBuildPulledService_FlagsFilteredToManifestNames(t *testing.T) {
	t.Parallel()
	// Synthesise a valkey-style manifest entry where add.Input.Flags
	// declares `secured`; verify the show response's `flags` map is
	// kept but trimmed to that single declared key (read-only flag
	// subkeys like apacheAge get dropped).
	m := fakeValkeyLikeManifest(t)
	addCmd, _ := AddCommand(m, "valkey")
	updateCmd, _ := UpdateCommand(m, "valkey")

	raw := map[string]any{
		"id":   "vsid1",
		"name": "cache",
		"flags": map[string]any{
			"secured":    true,  // declared in add.Input.Flags
			"apacheAge":  false, // not declared, should be dropped
			"vector":     true,  // not declared, should be dropped
		},
	}
	got := BuildPulledService(raw, "valkey", "mycluster3", "acct1", "vsid1", addCmd, updateCmd)

	flags, ok := got.Fields["flags"].(map[string]any)
	if !ok {
		t.Fatalf("expected flags map in yaml fields, got %T", got.Fields["flags"])
	}
	if v, ok := flags["secured"].(bool); !ok || !v {
		t.Errorf("expected secured=true, got %v", flags["secured"])
	}
	for _, banned := range []string{"apacheAge", "vector"} {
		if _, ok := flags[banned]; ok {
			t.Errorf("flag %q should not appear in yaml (not in add.Input.Flags)", banned)
		}
	}
}

func TestBuildPulledService_NoFlagsSectionWhenNoneDeclared(t *testing.T) {
	t.Parallel()
	// Postgres has no Input.Flags in our fake manifest. Even if the
	// show response carries a `flags` map (with read-only subkeys),
	// the yaml should not contain `flags:` at all.
	m := fakeManifest(t)
	addCmd, _ := AddCommand(m, "postgresql")
	updateCmd, _ := UpdateCommand(m, "postgresql")
	raw := map[string]any{
		"id":    "abc12",
		"name":  "x",
		"flags": map[string]any{"apacheAge": false, "vector": false},
	}
	got := BuildPulledService(raw, "postgresql", "mycluster3", "acct1", "abc12", addCmd, updateCmd)
	if _, ok := got.Fields["flags"]; ok {
		t.Errorf("postgres yaml should not carry flags: (no add.Input.Flags), got %v", got.Fields["flags"])
	}
}
