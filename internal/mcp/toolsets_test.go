package mcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

func testManifest() *manifest.Manifest {
	return &manifest.Manifest{Commands: []manifest.Command{
		{Command: "services/postgresql/list", MCP: []string{"read"}},
		{Command: "services/kafka/list", MCP: []string{"read"}},
		{Command: "services/cert-manager/list", MCP: []string{"read"}},
		{Command: "services/traefik/list", MCP: []string{"read"}},
		{Command: "services/wireguard/list", MCP: []string{"read"}},
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
	// Conductor sends these; the CLI no longer carries its own copy.
	for _, x := range []string{"cert-manager", "traefik", "wireguard"} {
		ts.platformManaged[x] = struct{}{}
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
	// kafka is hidden because unused; the three platform services are hidden
	// despite being installable, so all four are offerable.
	got := ts.Hidden()
	if !slices.Contains(got, "kafka") {
		t.Errorf("Hidden() = %v, want it to include kafka", got)
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
	// With nothing hidden at all the description must not claim otherwise. That
	// needs every type enabled, because infra types are hidden even when in use.
	all := scopedTo("postgresql", "kafka")
	all.Enable([]string{"cert-manager", "traefik", "wireguard"})
	if len(all.Hidden()) != 0 {
		t.Fatalf("fixture still hides %v", all.Hidden())
	}
	if strings.Contains(all.enableToolDescription(), "NOT loaded") {
		t.Error("described types as hidden when none are")
	}
}

// virt is an account MODULE now (FPL31), not a toolsets capability. Conductor
// leaves a disabled module's commands out of the account-scoped manifest, so
// there is nothing here to hide and nothing here to enable. The old
// `runos_tools_enable virt` spelling has to stop working rather than silently
// do nothing, or an agent reading a stale topic would believe it had loaded a
// surface it does not have.
func TestVirtIsNotAToolsetCapability(t *testing.T) {
	ts := scopedTo("postgresql")

	added, unknown := ts.Enable([]string{"virt"})
	if len(added) != 0 {
		t.Errorf("Enable(virt) added %v; virt is not a service type", added)
	}
	if len(unknown) != 1 || unknown[0] != "virt" {
		t.Errorf("Enable(virt) unknown = %v, want it named back as unknown", unknown)
	}

	if slices.Contains(ts.Hidden(), "virt") {
		t.Errorf("Hidden() offers virt: %v", ts.Hidden())
	}

	// The VM commands are governed by the manifest conductor served, so a
	// command that reached this CLI is exposed.
	for _, p := range []string{"vms/list", "vm-groups/list", "vm-images/list", "vm-usage"} {
		if !ts.permits(p) {
			t.Errorf("toolsets still hides %s; only conductor gates the module", p)
		}
	}
}

// The platform installs and runs these, so they are hidden even though the
// account genuinely has them. They must stay reachable, because cert-manager is
// what a stuck certificate needs and wireguard is what a node delete moves.
func TestPlatformServicesAreHiddenEvenWhenRunning(t *testing.T) {
	ts := scopedTo("postgresql", "cert-manager", "traefik", "wireguard")
	for _, p := range []string{"services/cert-manager/list", "services/traefik/list", "services/wireguard/list"} {
		if ts.permits(p) {
			t.Errorf("exposed platform service %s", p)
		}
	}
	if !ts.permits("services/postgresql/list") {
		t.Error("hid a service the account actually chose")
	}
	hidden := ts.Hidden()
	for _, want := range []string{"cert-manager", "traefik", "wireguard"} {
		if !slices.Contains(hidden, want) {
			t.Errorf("Hidden() omits %s, so nothing tells the caller it can be loaded: %v", want, hidden)
		}
	}
	// And enabling one has to work, despite it being "in use".
	if added, _ := ts.Enable([]string{"cert-manager"}); len(added) != 1 {
		t.Error("could not enable a hidden platform service")
	}
	if !ts.permits("services/cert-manager/list") {
		t.Error("cert-manager still hidden after being enabled")
	}
}

// The platform-owned list comes from conductor. A CLI-side copy would drift the
// moment a type is added or reclassified there, hiding something conductor no
// longer owns or missing one it does.
func TestPlatformManagedListComesFromConductorNotFromHere(t *testing.T) {
	ts := newUnscoped(testManifest())
	ts.scoped = true
	// Conductor said nothing is platform-owned, so nothing is hidden on that
	// basis, even for the types this CLI used to hardcode.
	ts.inUse["cert-manager"] = struct{}{}
	if !ts.permits("services/cert-manager/list") {
		t.Error("hid cert-manager although conductor did not mark it platform-owned")
	}
	// And conductor can mark something this CLI never knew about.
	ts2 := newUnscoped(testManifest())
	ts2.scoped = true
	ts2.inUse["postgresql"] = struct{}{}
	ts2.platformManaged["postgresql"] = struct{}{}
	if ts2.permits("services/postgresql/list") {
		t.Error("exposed a type conductor marked platform-owned")
	}
}

// Reading the kafka topic and then calling a kafka tool that is not listed is a
// dead end. The topic is where the agent has decided it wants the thing, so it
// is where the notice belongs.
func TestReadingAHiddenServiceTopicSaysTheToolsCanBeLoaded(t *testing.T) {
	ts := scopedTo("postgresql")
	got := ts.hiddenTypeNotice(map[string]any{"key": "kafka"})
	if got == "" {
		t.Fatal("no notice on a hidden service type")
	}
	for _, want := range []string{"runos_tools_enable", "kafka", "Do NOT install"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice omits %q: %s", want, got)
		}
	}
	// RunOS supporting it is the point; the notice must not read as absence.
	if !strings.Contains(strings.ToLower(got), "runos supports") {
		t.Errorf("notice does not say RunOS supports it: %s", got)
	}
}

// A notice that fires when it should not is worse than one that never fires:
// it would tell the agent to enable something already loaded.
func TestNoNoticeWhenTheToolsAreAlreadyThere(t *testing.T) {
	ts := scopedTo("postgresql")
	for _, args := range []map[string]any{
		{"key": "postgresql"},        // in use
		{"key": "platform-overview"}, // not a service type at all
		{"key": ""},                  // no key and no keywords
		{"keywords": "deploy"},       // a search naming nothing hidden
	} {
		if got := ts.hiddenTypeNotice(args); got != "" {
			t.Errorf("unexpected notice for %v: %s", args, got)
		}
	}
	// And once enabled, it stops.
	ts.Enable([]string{"kafka"})
	if got := ts.hiddenTypeNotice(map[string]any{"key": "kafka"}); got != "" {
		t.Errorf("still nagging after enable: %s", got)
	}
}

// Unscoped hides nothing, so it must never claim a type is unavailable.
func TestNoNoticeWhenUnscoped(t *testing.T) {
	if got := newUnscoped(testManifest()).hiddenTypeNotice(map[string]any{"key": "kafka"}); got != "" {
		t.Errorf("unscoped server produced a notice: %s", got)
	}
	var nilTS *Toolsets
	if got := nilTS.hiddenTypeNotice(map[string]any{"key": "kafka"}); got != "" {
		t.Errorf("nil Toolsets produced a notice: %s", got)
	}
}

// A platform-owned type is hidden despite being in use, so its topic must also
// say how to load it.
func TestPlatformServiceTopicAlsoGetsTheNotice(t *testing.T) {
	ts := scopedTo("postgresql", "cert-manager")
	if got := ts.hiddenTypeNotice(map[string]any{"key": "cert-manager"}); got == "" {
		t.Error("no notice on a hidden platform service topic")
	}
}

// Only 5 of 19 hidden types have a topic, so a read-only hook misses the case
// that matters: an agent looking for Kafka searches, finds nothing, and reads
// that as "RunOS has no Kafka".
func TestSearchingForAHiddenServiceSaysItExists(t *testing.T) {
	ts := scopedTo("postgresql")
	for _, kw := range []string{"kafka", "kafka,queue", "queue, kafka", "KAFKA"} {
		got := ts.hiddenTypeNotice(map[string]any{"keywords": kw})
		if got == "" {
			t.Errorf("search %q produced no notice", kw)
			continue
		}
		if !strings.Contains(got, "SUPPORTS kafka") {
			t.Errorf("search %q did not say RunOS supports it: %s", kw, got)
		}
	}
	// A search that names nothing hidden stays quiet.
	for _, kw := range []string{"deploy", "postgresql", "dockerfile"} {
		if got := ts.hiddenTypeNotice(map[string]any{"keywords": kw}); got != "" {
			t.Errorf("search %q produced a spurious notice: %s", kw, got)
		}
	}
}
