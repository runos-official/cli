package mcp

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func testManifest() *manifest.Manifest {
	return &manifest.Manifest{Commands: []manifest.Command{
		{Command: "services/postgresql/list", MCP: []string{"read"}},
		{Command: "services/kafka/list", MCP: []string{"read"}},
		{Command: "services/list", MCP: []string{"read"}},
		{Command: "services/dependencies", MCP: []string{"read"}},
		{Command: "services/{type}/{id}/show", MCP: []string{"read"}},
		{Command: "nodes/list", MCP: []string{"read"}},
	}}
}

// The type comes from the manifest PATH, never from the tool name. A regex over
// names would have to special-case `services_list` and `services_dependencies`
// by accident; both are generic and must always be exposed.
func TestOnlyTypedServicePathsCarryAType(t *testing.T) {
	for path, want := range map[string]string{
		"services/postgresql/list":  "postgresql",
		"services/netbird-server/x": "netbird-server",
		"services/list":             "",
		"services/dependencies":     "",
		"services/{type}/{id}/show": "",
		"nodes/list":                "",
		"":                          "",
	} {
		if got := serviceTypeOf(path); got != want {
			t.Errorf("serviceTypeOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// Every failure path must expose everything. A scoping mechanism that hides an
// operator's own platform when a read fails is worse than no scoping at all.
func TestUnscopedExposesEverything(t *testing.T) {
	ts := newUnscoped(testManifest())
	for _, p := range []string{"services/postgresql/list", "services/kafka/list", "nodes/list"} {
		if !ts.permits(p) {
			t.Errorf("unscoped server hid %s", p)
		}
	}
	if ts.Scoped() {
		t.Error("newUnscoped reported itself scoped")
	}
	if ts.Hidden() != nil {
		t.Error("an unscoped server should hide nothing")
	}
}

// A nil Toolsets is the shape a Server built directly has. It must behave as
// unscoped, not panic.
func TestNilToolsetsIsUnscoped(t *testing.T) {
	var ts *Toolsets
	if !ts.permits("services/kafka/list") {
		t.Error("nil Toolsets refused a tool")
	}
	if ts.Scoped() || ts.Hidden() != nil {
		t.Error("nil Toolsets should be unscoped and hide nothing")
	}
}

func scopedTo(types ...string) *Toolsets {
	ts := newUnscoped(testManifest())
	for _, x := range types {
		ts.inUse[x] = struct{}{}
	}
	ts.scoped = true
	return ts
}

func TestScopedHidesUnusedTypesButKeepsGenericTools(t *testing.T) {
	ts := scopedTo("postgresql")
	if !ts.permits("services/postgresql/list") {
		t.Error("hid a type the account runs")
	}
	if ts.permits("services/kafka/list") {
		t.Error("exposed a type the account does not run")
	}
	for _, generic := range []string{"services/list", "services/dependencies", "nodes/list"} {
		if !ts.permits(generic) {
			t.Errorf("hid the generic tool %s", generic)
		}
	}
	if got := ts.Hidden(); len(got) != 1 || got[0] != "kafka" {
		t.Errorf("Hidden() = %v, want [kafka]", got)
	}
}

// Enable widens the surface for the session and is idempotent.
func TestEnableAddsATypeAndRejectsAnUnknownOne(t *testing.T) {
	ts := scopedTo("postgresql")
	added, unknown := ts.Enable([]string{"kafka"})
	if len(added) != 1 || added[0] != "kafka" || len(unknown) != 0 {
		t.Fatalf("Enable(kafka) = %v, %v", added, unknown)
	}
	if !ts.permits("services/kafka/list") {
		t.Error("kafka still hidden after Enable")
	}
	if again, _ := ts.Enable([]string{"kafka"}); len(again) != 0 {
		t.Errorf("second Enable reported %v, want nothing added", again)
	}
	// A type with no commands in the manifest is named back, not silently
	// accepted, or the caller never learns it misspelled something.
	_, unknown = ts.Enable([]string{"nosuchservice"})
	if len(unknown) != 1 {
		t.Errorf("unknown type not reported: %v", unknown)
	}
}

// The prefixes are what an agent naturally types when it has been reading
// manifest paths or tool names.
func TestEnableAcceptsTheSpellingsAnAgentWillUse(t *testing.T) {
	for _, spelling := range []string{"kafka", "KAFKA", "services/kafka", "services_kafka", " kafka "} {
		ts := scopedTo("postgresql")
		if added, _ := ts.Enable([]string{spelling}); len(added) != 1 {
			t.Errorf("Enable(%q) added nothing", spelling)
		}
	}
}

// The description names the hidden types inline, so widening the surface costs
// no discovery round trip.
func TestEnableDescriptionNamesTheHiddenTypes(t *testing.T) {
	got := scopedTo("postgresql").enableToolDescription()
	if !strings.Contains(got, "kafka") {
		t.Errorf("description does not name the hidden type: %s", got)
	}
	if strings.Contains(scopedTo("postgresql", "kafka").enableToolDescription(), "NOT loaded") {
		t.Error("described types as hidden when none are")
	}
}
