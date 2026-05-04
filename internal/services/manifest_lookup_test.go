package services

import (
	"slices"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// namesAsFields builds a []manifest.OutputField from bare strings, for
// terse fixture construction in tests. The Output.Fields type accepts
// either bare strings or rich objects on the wire; tests rarely care
// about anything beyond the names.
func namesAsFields(names ...string) []manifest.OutputField {
	out := make([]manifest.OutputField, len(names))
	for i, n := range names {
		out[i].Name = n
	}
	return out
}

// manifestWithFullResourcesUpdateFields returns a manifest declaring a
// `traefik` type whose update endpoint accepts every class-coupled
// field (replicas + cpu/memory request/limit) plus resourceRequirementClassId
// itself. Used to drive the preset-wins gate in BuildPulledService through
// every field it can strip.
func manifestWithFullResourcesUpdateFields(t *testing.T) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Version: "test",
		Commands: []manifest.Command{
			{
				Command:  "services/traefik/{id}/show",
				Method:   "GET",
				Endpoint: "/:aid/:cid/services/traefik/:id",
				Output:   &manifest.Output{Fields: namesAsFields("id", "name", "version", "resourceRequirementClassId", "replicas", "cpuRequestMc", "cpuLimitMc", "memoryRequestMb", "memoryLimitMb")},
			},
			{
				Command:  "services/traefik/add",
				Method:   "POST",
				Endpoint: "/:aid/:cid/services/traefik",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "name", Type: "string"},
					{Name: "resourceRequirementClassId", Type: "string"},
				}},
			},
			{
				Command:  "services/traefik/{id}/update",
				Method:   "PATCH",
				Endpoint: "/:aid/:cid/services/traefik/:id",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "string", Required: true, Positional: true},
					{Name: "name", Type: "string"},
					{Name: "version", Type: "string"},
					{Name: "resourceRequirementClassId", Type: "string"},
					{Name: "replicas", Type: "integer"},
					{Name: "cpuRequestMc", Type: "integer"},
					{Name: "cpuLimitMc", Type: "integer"},
					{Name: "memoryRequestMb", Type: "integer"},
					{Name: "memoryLimitMb", Type: "integer"},
				}},
			},
		},
	}
}

// fakeValkeyLikeManifest returns a minimal manifest with a `valkey` type
// whose add command declares Input.Flags. Used to exercise the flags
// projection path in BuildPulledService.
func fakeValkeyLikeManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Version: "test",
		Commands: []manifest.Command{
			{
				Command:  "services/valkey/{id}/show",
				Method:   "GET",
				Endpoint: "/:aid/:cid/services/valkey/:id",
				Output:   &manifest.Output{Fields: namesAsFields("id", "name", "flags")},
			},
			{
				Command:  "services/valkey/add",
				Method:   "POST",
				Endpoint: "/:aid/:cid/services/valkey",
				Input: &manifest.Input{
					Fields: []manifest.Field{
						{Name: "name", Type: "string"},
					},
					Flags: []manifest.Flag{
						{Name: "secured", Default: true},
					},
				},
			},
			{
				Command:  "services/valkey/{id}/update",
				Method:   "PATCH",
				Endpoint: "/:aid/:cid/services/valkey/:id",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "string", Required: true, Positional: true},
					{Name: "name", Type: "string"},
				}},
			},
		},
	}
}

// fakeManifest returns a minimal manifest containing the three commands
// services_pull/diff/sync need for postgres + an incomplete entry for
// "harbor" (only show, missing add+update) so tests can verify
// ListSupportedTypes filters correctly.
func fakeManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Version: "test",
		Commands: []manifest.Command{
			{
				Command:  "services/postgresql/{id}/show",
				Method:   "GET",
				Endpoint: "/:aid/:cid/services/postgresql/:id",
				Output:   &manifest.Output{Fields: namesAsFields("id", "name", "version")},
			},
			{
				Command:  "services/postgresql/add",
				Method:   "POST",
				Endpoint: "/:aid/:cid/services/postgresql",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "name", Type: "string"},
					{Name: "resourceRequirementClassId", Type: "string"},
				}},
			},
			{
				Command:  "services/postgresql/{id}/update",
				Method:   "PATCH",
				Endpoint: "/:aid/:cid/services/postgresql/:id",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "id", Type: "string", Required: true, Positional: true},
					{Name: "name", Type: "string"},
					{Name: "replicas", Type: "integer"},
					{Name: "version", Type: "string"},
				}},
			},
			// harbor: only has show, ListSupportedTypes should filter it out.
			{
				Command:  "services/harbor/{id}/show",
				Method:   "GET",
				Endpoint: "/:aid/:cid/services/harbor/:id",
			},
		},
	}
}

func TestListSupportedTypes(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	got := ListSupportedTypes(m)
	if !slices.Equal(got, []string{"postgresql"}) {
		t.Errorf("expected [postgresql], got %v", got)
	}
}

func TestIsSupportedType(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	if !IsSupportedType(m, "postgresql") {
		t.Error("postgresql should be supported")
	}
	if IsSupportedType(m, "harbor") {
		t.Error("harbor should not be supported (only show is in manifest)")
	}
	if IsSupportedType(m, "unknown") {
		t.Error("unknown type should not be supported")
	}
}

func TestUpdateInputFieldNames(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	cmd, err := UpdateCommand(m, "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	got := UpdateInputFieldNames(cmd)
	// id is positional so it should be excluded; name+replicas should be in.
	if got["id"] {
		t.Error("id should be excluded from input fields (positional)")
	}
	if !got["name"] || !got["replicas"] {
		t.Errorf("expected name+replicas in fields, got %v", got)
	}
}

func TestShowOutputFields(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	cmd, err := ShowCommand(m, "postgresql")
	if err != nil {
		t.Fatal(err)
	}
	got := ShowOutputFields(cmd)
	if !slices.Equal(got, []string{"id", "name", "version"}) {
		t.Errorf("unexpected output fields: %v", got)
	}
}

func TestFindCommandNotFound(t *testing.T) {
	t.Parallel()
	m := fakeManifest(t)
	_, err := ShowCommand(m, "nonexistent")
	if err == nil {
		t.Error("expected error for unknown service type")
	}
}
