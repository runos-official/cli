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
	// I9-F: `replicas` is no longer in the preset-wins strip set
	// (cpuRequestMc / cpuLimitMc / memoryRequestMb / memoryLimitMb
	// still are, but those are filtered above by the audit-fields gate
	// when they happen to live alongside a class - here cpuLimitMc and
	// memoryLimitMb were declared audit by the synthetic case).
	// Replicas IS in the update endpoint's Input.Fields, so it stays
	// in the projection regardless of stored class. See the dedicated
	// preset-wins test below for the remaining class-coupled set.
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

// TestBuildPulledService_PresetWinsStripsClassCoupledFields verifies the
// preset-wins gate: when the stored resourceRequirementClassId is a real
// class, the cpu/memory/replica fields the class encapsulates server-side
// are dropped from the yaml projection. When the class is "custom" (or
// empty / legacy), those fields are kept because the user has explicitly
// diverged and the values are the source of truth.
//
// Without this gate the yaml round-trips with both class AND derived
// values, producing false drift on diff and (on sync) flipping the
// server-stored class to "custom" because the API treats class +
// override as a custom config.
func TestBuildPulledService_PresetWinsStripsClassCoupledFields(t *testing.T) {
	t.Parallel()
	// Build a manifest that declares ALL class-coupled fields as
	// settable on update, so the projection sees them and the gate
	// has something to strip. The fakeManifest only has replicas, but
	// real types like traefik declare cpu/memory too.
	m := manifestWithFullResourcesUpdateFields(t)
	addCmd, _ := AddCommand(m, "traefik")
	updateCmd, _ := UpdateCommand(m, "traefik")

	rawWithRealClass := map[string]any{
		"id":                         "ek6is",
		"name":                       "traefik-ek6is",
		"resourceRequirementClassId": "traefik.c0.beff",
		"version":                    "v3.6.10",
		"replicas":                   1,
		"cpuRequestMc":               0,
		"cpuLimitMc":                 250,
		"memoryRequestMb":            0,
		"memoryLimitMb":              256,
	}
	got := BuildPulledService(rawWithRealClass, "traefik", "mycluster3", "acct1", "ek6is", addCmd, updateCmd)
	// I9-F: replicas is no longer stripped on a real class (it stays
	// so the diff projection has a server-side value to compare against
	// and the user can read their current scale from the yaml).
	if got.Fields["replicas"] != 1 {
		t.Errorf("real class: replicas should be kept post-I9-F, got %v", got.Fields["replicas"])
	}
	for _, banned := range []string{"cpuRequestMc", "cpuLimitMc", "memoryRequestMb", "memoryLimitMb"} {
		if _, ok := got.Fields[banned]; ok {
			t.Errorf("real class: field %q must be stripped (class encapsulates it), got %v", banned, got.Fields[banned])
		}
	}
	if got.Fields["resourceRequirementClassId"] != "traefik.c0.beff" {
		t.Errorf("real class: resourceRequirementClassId = %v, want traefik.c0.beff", got.Fields["resourceRequirementClassId"])
	}

	rawWithCustom := map[string]any{
		"id":                         "ek6is",
		"name":                       "traefik-ek6is",
		"resourceRequirementClassId": "custom",
		"version":                    "v3.6.10",
		"replicas":                   2,
		"cpuRequestMc":               1000,
		"cpuLimitMc":                 4000,
		"memoryRequestMb":            512,
		"memoryLimitMb":              4096,
	}
	got = BuildPulledService(rawWithCustom, "traefik", "mycluster3", "acct1", "ek6is", addCmd, updateCmd)
	for _, kept := range []string{"replicas", "cpuRequestMc", "cpuLimitMc", "memoryRequestMb", "memoryLimitMb"} {
		if _, ok := got.Fields[kept]; !ok {
			t.Errorf("custom class: field %q must be kept (user-owned values), absent", kept)
		}
	}

	// Empty class (legacy / pre-class data): keep cpu/memory/replicas
	// because they're the only signal of how the service is sized.
	rawNoClass := map[string]any{
		"id":              "ek6is",
		"name":            "traefik-ek6is",
		"replicas":        1,
		"cpuLimitMc":      250,
		"memoryLimitMb":   256,
		"cpuRequestMc":    0,
		"memoryRequestMb": 0,
	}
	got = BuildPulledService(rawNoClass, "traefik", "mycluster3", "acct1", "ek6is", addCmd, updateCmd)
	for _, kept := range []string{"replicas", "cpuRequestMc", "cpuLimitMc", "memoryRequestMb", "memoryLimitMb"} {
		if _, ok := got.Fields[kept]; !ok {
			t.Errorf("empty class: field %q must be kept (legacy fallback), absent", kept)
		}
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
			"secured":   true,  // declared in add.Input.Flags
			"apacheAge": false, // not declared, should be dropped
			"vector":    true,  // not declared, should be dropped
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
