package apps

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Regression test for I2-4c (TEST_LOG.md): the pre-deploy "fields will
// be CLEARED" warning previously hardcoded the clear families
// (healthCheck*/metrics*) regardless of what was actually in scope. The
// partition helper feeds the warning so the listed clearing fields
// always match the warning text. cpu* / memory* / replicas /
// clusterDomainId are partial-update on the conductor's PATCH
// endpoint, so they must NOT be classified as clearing.
func TestPartitionServerOnlyByClearSemantics(t *testing.T) {
	cases := []struct {
		name             string
		serverOnly       []string
		wantClear        []string
		wantPreserveOnly []string
	}{
		{
			name:             "I2-4c canonical: cpu/memory only",
			serverOnly:       []string{"cpuLimitMc", "cpuRequestMc", "memoryLimitMb", "memoryRequestMb"},
			wantClear:        nil,
			wantPreserveOnly: []string{"cpuLimitMc", "cpuRequestMc", "memoryLimitMb", "memoryRequestMb"},
		},
		{
			name:             "all five omit-clear fields",
			serverOnly:       []string{"healthCheck", "healthCheckPort", "healthCheckPath", "metricsPort", "metricsPath"},
			wantClear:        []string{"healthCheck", "healthCheckPort", "healthCheckPath", "metricsPort", "metricsPath"},
			wantPreserveOnly: nil,
		},
		{
			name:             "mixed: some clear, some preserve",
			serverOnly:       []string{"healthCheckPath", "cpuLimitMc", "metricsPort", "replicas", "clusterDomainId"},
			wantClear:        []string{"healthCheckPath", "metricsPort"},
			wantPreserveOnly: []string{"cpuLimitMc", "replicas", "clusterDomainId"},
		},
		{
			name:             "nested non-domain paths classified by top-level only",
			serverOnly:       []string{"requires.foo.config", "healthCheckPort"},
			wantClear:        []string{"healthCheckPort"},
			wantPreserveOnly: []string{"requires.foo.config"},
		},
		{
			// Regression test for I2-4e' (TEST_LOG.md): a removed
			// `servicePortMappings[].domains` entry is omit-deletes,
			// not preserve. Earlier versions of the partition lumped
			// it under "Preserved server-side" because the top-level
			// extractor saw `servicePortMappings` and that wasn't in
			// OmitClearFields. The IsOmitClearPath nested matcher
			// catches it now.
			name:             "I2-4e' regression: servicePortMappings[N].domains is clear",
			serverOnly:       []string{`servicePortMappings[0].domains (1 entry)`, `servicePortMappings[0].port (3000)`},
			wantClear:        []string{`servicePortMappings[0].domains (1 entry)`},
			wantPreserveOnly: []string{`servicePortMappings[0].port (3000)`},
		},
		{
			// I2-4e' partner: top-level `domain:` is also omit-
			// deletes per the iter-2 conductor decision. Must NOT
			// land in the preserve bucket.
			name:             "I2-4e' regression: top-level domain is clear",
			serverOnly:       []string{`domain ("example.com")`},
			wantClear:        []string{`domain ("example.com")`},
			wantPreserveOnly: nil,
		},
		{
			name:             "empty input",
			serverOnly:       nil,
			wantClear:        nil,
			wantPreserveOnly: nil,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gotClear, gotPreserve := PartitionServerOnlyByClearSemantics(tt.serverOnly)
			if !reflect.DeepEqual(gotClear, tt.wantClear) {
				t.Errorf("clearOnOmit: got %v, want %v", gotClear, tt.wantClear)
			}
			if !reflect.DeepEqual(gotPreserve, tt.wantPreserveOnly) {
				t.Errorf("preserveOnOmit: got %v, want %v", gotPreserve, tt.wantPreserveOnly)
			}
		})
	}
}

// I3-E retest follow-up: classify each server-only entry as
// blocking (refuses deploy without --force) or benign (waved through
// when AdditiveOnly). Pinned per shape so a future formatter change
// to summarizeValue can't silently inflate the benign set.
func TestIsBenignPreserveZero(t *testing.T) {
	cases := []struct {
		entry string
		want  bool
		why   string
	}{
		// Benign zero defaults that must NOT block deploy.
		{"cpuRequestMc (0)", true, "preserve-on-omit numeric zero"},
		{"cpuLimitMc (0)", true, "preserve-on-omit numeric zero"},
		{"memoryRequestMb (0)", true, "preserve-on-omit numeric zero"},
		{"memoryLimitMb (0)", true, "preserve-on-omit numeric zero"},
		{"replicas (0)", true, "preserve-on-omit numeric zero"},
		{`clusterDomainId ("")`, true, "preserve-on-omit empty string"},
		{"someBoolField (false)", true, "preserve-on-omit zero bool"},
		{"someListField (0 entries)", true, "preserve-on-omit empty list"},
		{"someMapField (0 fields)", true, "preserve-on-omit empty map"},
		{"someNullableField (null)", true, "preserve-on-omit null"},
		// Non-zero preserve-on-omit values: blocking (user might want
		// to preserve a server-side customisation).
		{"cpuRequestMc (500)", false, "preserve-on-omit but non-zero"},
		{"memoryRequestMb (256)", false, "preserve-on-omit but non-zero"},
		{`resourceRequirementClassId ("custom")`, false, "preserve-on-omit non-empty string"},
		{`clusterDomainId ("elpfn")`, false, "preserve-on-omit non-empty string"},
		// clearOnOmit fields: never benign (omit wipes them, scary).
		{`domain ("foo.com")`, false, "clearOnOmit, never benign"},
		{`healthCheck ("startup")`, false, "clearOnOmit, never benign"},
		{"healthCheckPort (3000)", false, "clearOnOmit, never benign"},
		{`healthCheckPath ("/healthz")`, false, "clearOnOmit, never benign"},
		{"metricsPort (9090)", false, "clearOnOmit, never benign"},
		{`metricsPath ("/metrics")`, false, "clearOnOmit, never benign"},
		// clearOnOmit at zero: still blocking — omit semantics matter
		// even when the value is the zero default.
		{"healthCheckPort (0)", false, "clearOnOmit at zero, still not benign"},
		// Nested clearOnOmit (servicePortMappings[N].domains).
		{`servicePortMappings[0].domains (1 entry)`, false, "nested clearOnOmit"},
		// Entry with no value summary parses to name only; absent
		// summary is not zero (no signal), so blocking.
		{"someField", false, "no summary, no zero signal"},
		// Empty entry.
		{"", false, "empty entry"},
	}
	for _, c := range cases {
		t.Run(c.entry, func(t *testing.T) {
			if got := IsBenignPreserveZero(c.entry); got != c.want {
				t.Errorf("IsBenignPreserveZero(%q) = %v, want %v (%s)", c.entry, got, c.want, c.why)
			}
		})
	}
}

// I4-F regression: `requires` server-only fields must drop out of
// the pre-deploy gate's bulleted summary so the user doesn't read
// "Preserved server-side: requires.<alias>" right after they
// intentionally removed the alias. The conductor's
// `replaceDependencies` step handles requires edge removal
// regardless of the apps PATCH's omit-clear semantics, so the
// classification is structurally inappropriate.
func TestIsOrchestrationRemovedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Top-level requires entry.
		{"requires", true},
		// Aliased requires entries (the typical listServerOnlyFields shape).
		{"requires.shortlinks-dev-cache", true},
		{"requires.poll-app-db", true},
		// Summary-suffixed forms produced by the walker.
		{"requires (3 fields)", true},
		// Nested deep requires shapes (defensive).
		{"requires.foo.config.databaseName", true},
		// Non-requires paths unaffected.
		{"replicas", false},
		{"cpuRequestMc", false},
		{"healthCheckPort", false},
		{"servicePortMappings[0].domains", false},
		{`domain ("foo.com")`, false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := IsOrchestrationRemovedPath(c.path); got != c.want {
				t.Errorf("IsOrchestrationRemovedPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestFilterOrchestrationRemoved(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty input", nil, nil},
		{
			"requires only: filtered to empty",
			[]string{"requires.cache (3 fields)"},
			[]string{},
		},
		{
			"mixed: requires dropped, others preserved",
			[]string{
				"cpuRequestMc (0)",
				"requires.cache (3 fields)",
				`domain ("foo.com")`,
				"requires.db (4 fields)",
				"healthCheckPort (3000)",
			},
			[]string{
				"cpuRequestMc (0)",
				`domain ("foo.com")`,
				"healthCheckPort (3000)",
			},
		},
		{
			"all non-requires: passthrough",
			[]string{"cpuRequestMc (0)", `domain ("foo.com")`},
			[]string{"cpuRequestMc (0)", `domain ("foo.com")`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FilterOrchestrationRemoved(c.in)
			if !reflect.DeepEqual(got, c.want) {
				// Tolerate nil vs empty-slice equivalence: callers
				// only iterate, length is what matters.
				if len(got) == 0 && len(c.want) == 0 {
					return
				}
				t.Errorf("FilterOrchestrationRemoved(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// I4-F partner: FilterOrchestrationRemoved must not mutate its input.
func TestFilterOrchestrationRemoved_DoesNotMutateInput(t *testing.T) {
	input := []string{"cpuRequestMc (0)", "requires.cache (3 fields)", `domain ("foo.com")`}
	original := append([]string(nil), input...)
	_ = FilterOrchestrationRemoved(input)
	if !reflect.DeepEqual(input, original) {
		t.Errorf("input mutated: got %v, want %v", input, original)
	}
}

func TestAnyServerOnlyIsBlocking(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    bool
	}{
		{"empty list", nil, false},
		{"all benign zeros", []string{"cpuRequestMc (0)", "memoryRequestMb (0)"}, false},
		{"single clearOnOmit", []string{`domain ("foo.com")`}, true},
		{"single non-zero preserve-on-omit", []string{"cpuRequestMc (500)"}, true},
		{"mixed benign + clearOnOmit", []string{"cpuRequestMc (0)", `domain ("x")`}, true},
		{"mixed benign + non-zero", []string{"cpuRequestMc (0)", "memoryRequestMb (256)"}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyServerOnlyIsBlocking(tt.entries); got != tt.want {
				t.Errorf("AnyServerOnlyIsBlocking(%v) = %v, want %v", tt.entries, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// computeEnvChange
// ---------------------------------------------------------------------------

func TestComputeEnvChange_NoOpWhenEqual(t *testing.T) {
	got := computeEnvChange(
		map[string]string{"A": "1", "B": "2"},
		map[string]string{"A": "1", "B": "2"},
		nil,
	)
	if got != nil {
		t.Fatalf("expected nil for no-op, got %+v", got)
	}
}

func TestComputeEnvChange_ClassifiesAddUpdateRemove(t *testing.T) {
	got := computeEnvChange(
		map[string]string{"NEW": "yes", "CHANGED": "v2", "SAME": "ok"},
		map[string]string{"CHANGED": "v1", "SAME": "ok", "STALE": "x"},
		nil,
	)
	if got == nil {
		t.Fatal("expected change, got nil")
	}
	if got.Add["NEW"] != "yes" {
		t.Errorf("Add: %+v", got.Add)
	}
	if got.Update["CHANGED"] != "v2" {
		t.Errorf("Update: %+v", got.Update)
	}
	if len(got.Remove) != 1 || got.Remove[0] != "STALE" {
		t.Errorf("Remove: %+v", got.Remove)
	}
	// Final must equal the local map exactly, that's what gets sent.
	if len(got.Final) != 3 || got.Final["NEW"] != "yes" || got.Final["CHANGED"] != "v2" || got.Final["SAME"] != "ok" {
		t.Errorf("Final: %+v", got.Final)
	}
}

// Server-only keys claimed by some requires.<alias>.env mapping must land in
// PreservedByPlatform, not Remove. Conductor re-injects them on every push,
// so listing them as "replace-all will delete" is a lie. Keys not claimed by
// requires still go to Remove. Add/Update are unaffected by partitioning.
func TestComputeEnvChange_PartitionsRequiresInjectedFromRemove(t *testing.T) {
	platform := map[string]bool{"DATABASE_URL": true, "REDIS_HOST": true}
	got := computeEnvChange(
		map[string]string{"USER_VAR": "x"},
		map[string]string{"USER_VAR": "x", "DATABASE_URL": "postgres://...", "REDIS_HOST": "valkey:6379", "STALE_USER_VAR": "drop-me"},
		platform,
	)
	if got == nil {
		t.Fatal("expected change, got nil")
	}
	if len(got.Remove) != 1 || got.Remove[0] != "STALE_USER_VAR" {
		t.Errorf("Remove should contain only the user-authored stale key, got: %+v", got.Remove)
	}
	if len(got.PreservedByPlatform) != 2 ||
		got.PreservedByPlatform[0] != "DATABASE_URL" ||
		got.PreservedByPlatform[1] != "REDIS_HOST" {
		t.Errorf("PreservedByPlatform should hold the requires-injected names sorted, got: %+v", got.PreservedByPlatform)
	}
}

// A plan where only platform-injected names differ is a no-op: those keys
// always come back, so showing a section just to say "nothing's actually
// going to change" would be more noise than signal.
func TestComputeEnvChange_NoOpWhenOnlyPlatformInjectedDiffer(t *testing.T) {
	platform := map[string]bool{"DATABASE_URL": true}
	got := computeEnvChange(
		map[string]string{"USER_VAR": "x"},
		map[string]string{"USER_VAR": "x", "DATABASE_URL": "postgres://..."},
		platform,
	)
	if got != nil {
		t.Fatalf("expected nil when only platform-injected names differ, got: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// computeSecretFilesChange
// ---------------------------------------------------------------------------

func TestComputeSecretFilesChange_AddUpdateRemove(t *testing.T) {
	matchingContent := []byte("server-and-local-match\n")
	driftedContent := []byte("local-version\n")

	local := map[string][]byte{
		"match.crt":    matchingContent,
		"drift.crt":    driftedContent,
		"new-only.pem": []byte("brand-new\n"),
	}
	server := []SecretFileSummary{
		{Filename: "match.crt", MountPath: "/etc/match.crt", MD5: md5Hex(matchingContent)},
		{Filename: "drift.crt", MountPath: "/etc/drift.crt", MD5: "different-md5"},
		{Filename: "server-only.pem", MountPath: "/etc/server-only.pem", MD5: "abc"},
	}

	got := computeSecretFilesChange(nil, local, server)
	if got == nil {
		t.Fatal("expected change, got nil")
	}

	// match.crt: in both sides with same md5 → no entry anywhere.
	for _, p := range got.Add {
		if p.Filename == "match.crt" {
			t.Errorf("match.crt should not appear in Add")
		}
	}
	for _, p := range got.Update {
		if p.Filename == "match.crt" {
			t.Errorf("match.crt should not appear in Update")
		}
	}

	// drift.crt: same name on both, different bytes → Update, mountPath
	// preserved from server.
	if len(got.Update) != 1 || got.Update[0].Filename != "drift.crt" {
		t.Fatalf("Update: %+v", got.Update)
	}
	if got.Update[0].MountPath != "/etc/drift.crt" {
		t.Errorf("Update mount path should preserve server's: %q", got.Update[0].MountPath)
	}

	// new-only.pem: local-only → Add. mountPath defaults to /<filename>.
	if len(got.Add) != 1 || got.Add[0].Filename != "new-only.pem" {
		t.Fatalf("Add: %+v", got.Add)
	}

	// server-only.pem: server-only → Remove.
	if len(got.Remove) != 1 || got.Remove[0] != "server-only.pem" {
		t.Errorf("Remove: %+v", got.Remove)
	}
}

func TestComputeSecretFilesChange_NoOpWhenAllMatch(t *testing.T) {
	content := []byte("hello\n")
	got := computeSecretFilesChange(
		nil,
		map[string][]byte{"a.txt": content},
		[]SecretFileSummary{{Filename: "a.txt", MountPath: "/a.txt", MD5: md5Hex(content)}},
	)
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// computeOverrideOps
// ---------------------------------------------------------------------------

func TestComputeOverrideOps_AddUpdateDeleteAndUntouched(t *testing.T) {
	local := []LocalOverride{
		{ID: "", Name: "brand-new", Enabled: true, Content: []byte("new")},
		{ID: "stableId", Name: "unchanged", Enabled: true, Content: []byte("same")},
		{ID: "driftId", Name: "renamed-here", Enabled: false, Content: []byte("changed")},
	}
	server := []OverrideSummary{
		{ID: "stableId", Name: "unchanged", Enabled: true, Data: base64.StdEncoding.EncodeToString([]byte("same"))},
		{ID: "driftId", Name: "old-name", Enabled: true, Data: base64.StdEncoding.EncodeToString([]byte("server-version"))},
		{ID: "deletedId", Name: "no-longer-local", Enabled: true, Data: ""},
	}

	ops := computeOverrideOps(local, server)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops (add, update, delete), got %d: %+v", len(ops), ops)
	}

	byOp := map[string]OverrideOp{}
	for _, o := range ops {
		byOp[o.Op] = o
	}
	if byOp["add"].Name != "brand-new" {
		t.Errorf("add op: %+v", byOp["add"])
	}
	if byOp["update"].ID != "driftId" {
		t.Errorf("update op: %+v", byOp["update"])
	}
	if byOp["delete"].ID != "deletedId" {
		t.Errorf("delete op: %+v", byOp["delete"])
	}
	// The untouched override (stableId) should NOT produce an op.
	for _, o := range ops {
		if o.ID == "stableId" {
			t.Errorf("unchanged override should not produce an op: %+v", o)
		}
	}
}

// Round 4 T3 #1: an override removed from the yaml triggers a server-
// side delete via apps_sync, but the local file at <appDir>/overrides/
// <name>.yaml was left on disk. The fix populates LocalLeaf on delete
// ops so the apply path can unlink the local file alongside the server
// delete. LocalLeaf must match what `apps_pull` (via OverrideFilenames)
// would have written — collision-disambiguated names (`<name>-<shortID>`)
// included.
func TestComputeOverrideOps_DeletePopulatesLocalLeaf(t *testing.T) {
	local := []LocalOverride{} // user removed the override from yaml
	server := []OverrideSummary{
		{ID: "kept", Name: "stays", Enabled: true},
		{ID: "remove1", Name: "old-feature", Enabled: true},
		{ID: "remove2", Name: "another", Enabled: true},
	}
	// Local references "kept" so it doesn't get deleted.
	local = append(local, LocalOverride{ID: "kept", Name: "stays", Enabled: true,
		Content: []byte("body")})
	// Server "kept" matches local content.
	server[0].Data = base64.StdEncoding.EncodeToString([]byte("body"))

	ops := computeOverrideOps(local, server)
	deletes := []OverrideOp{}
	for _, o := range ops {
		if o.Op == "delete" {
			deletes = append(deletes, o)
		}
	}
	if len(deletes) != 2 {
		t.Fatalf("expected 2 delete ops, got %d: %+v", len(deletes), deletes)
	}
	for _, d := range deletes {
		if d.LocalLeaf == "" {
			t.Errorf("delete op for %q must populate LocalLeaf for cleanup, got %+v", d.Name, d)
		}
		if !strings.HasSuffix(d.LocalLeaf, ".yaml") {
			t.Errorf("LocalLeaf %q for %q should end in .yaml", d.LocalLeaf, d.Name)
		}
	}
}

// Defensive: when the server side has duplicate override names that
// would collide on the same filename, OverrideFilenames disambiguates
// with a `-<shortID>` suffix. The delete op's LocalLeaf must reflect
// that, otherwise a delete on the second-of-two-same-named overrides
// would target the wrong file (or no file).
func TestComputeOverrideOps_DeleteLocalLeafDisambiguatesCollisions(t *testing.T) {
	server := []OverrideSummary{
		{ID: "id-A", Name: "duplicate", Enabled: true},
		{ID: "id-B", Name: "duplicate", Enabled: true},
	}
	ops := computeOverrideOps(nil, server)
	if len(ops) != 2 {
		t.Fatalf("expected 2 delete ops, got %d", len(ops))
	}
	leaves := map[string]bool{}
	for _, o := range ops {
		if leaves[o.LocalLeaf] {
			t.Errorf("two delete ops share the same LocalLeaf %q (collision not disambiguated)", o.LocalLeaf)
		}
		leaves[o.LocalLeaf] = true
	}
}

func TestComputeOverrideOps_OrphanLocalIDBecomesAdd(t *testing.T) {
	// Local references an id that the server no longer knows about. The
	// planner should re-create it (add) rather than trying to update an
	// ID that's gone.
	local := []LocalOverride{{ID: "ghostId", Name: "x", Enabled: true, Content: []byte("body")}}
	ops := computeOverrideOps(local, nil)
	if len(ops) != 1 || ops[0].Op != "add" {
		t.Fatalf("expected single add op, got %+v", ops)
	}
	if ops[0].Reason == "" {
		t.Error("expected Reason to explain the recreate")
	}
}

// ---------------------------------------------------------------------------
// computeYAMLPatch
// ---------------------------------------------------------------------------

func TestComputeYAMLPatch_BodyIsFullLocalYAMLOnDrift(t *testing.T) {
	// Under the new conductor's desired-state semantics, the PATCH body
	// is the FULL local yaml, not a sparse diff. A partial body that
	// omits healthCheck/metrics would silently clear those fields
	// server-side. So when ANY drift is detected, every locally-set
	// field must show up in the wire body, including ones that already
	// match the server.
	local := &PulledApp{
		App:                        "my-app",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		Replicas:                   3, // the only field that differs
		ClusterDomainID:            "elpfn",
		ResourceRequirementClassID: "app.sl1.beff",
		ServicePortMappings:        []Port{{Port: 8080, StandardHttps: true}},
	}
	server := map[string]any{
		"name":                       "my-app",
		"clusterDomainId":            "elpfn",
		"resourceRequirementClassId": "app.sl1.beff",
		"replicas":                   float64(1),
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}
	patch, _, refused, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when replicas drifts")
	}
	for _, k := range []string{"name", "clusterDomainId", "resourceRequirementClassId", "replicas", "servicePortMappings"} {
		if _, ok := patch[k]; !ok {
			t.Errorf("body must include %q (full local yaml), got %+v", k, patch)
		}
	}
	if patch["replicas"] != 3 {
		t.Errorf("replicas should be 3 (local value), got %v", patch["replicas"])
	}
	if len(refused) != 0 {
		t.Errorf("no refused fields expected, got %v", refused)
	}
}

func TestComputeYAMLPatch_NoDriftReturnsNilPatch(t *testing.T) {
	// When local and server are in sync, the patch is nil so the sync
	// command can short-circuit the API call.
	local := &PulledApp{
		App:      "my-app",
		ID:       "ab12c",
		CID:      "k1",
		AID:      "acc-1",
		Replicas: 1,
	}
	server := map[string]any{
		"name":     "my-app",
		"replicas": float64(1),
	}
	patch, diff, refused, _ := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch (no drift), got %+v", patch)
	}
	if diff != "" {
		t.Errorf("expected empty diff, got %q", diff)
	}
	if len(refused) != 0 {
		t.Errorf("no refused fields expected, got %v", refused)
	}
}

// Partial-update fields (replicas, resourceRequirementClassId,
// clusterDomainId) follow omit=preserve on the conductor's PATCH endpoint:
// an omitted local value means "preserve server", not "clear / set to
// zero". The drift detector must mirror the wire body's omit logic so the
// dry-run plan doesn't render alarming `replicas: 1 -> replicas: 0` lines
// for a yaml that simply doesn't carry those fields.
func TestComputeYAMLPatch_OmittedReplicasIsNotDrift(t *testing.T) {
	local := &PulledApp{App: "my-app"} // Replicas omitted (zero value).
	server := map[string]any{
		"name":     "my-app",
		"replicas": float64(2),
	}
	patch, diff, _, _ := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch when only diff is server-replicas-vs-local-zero (preserve), got %+v", patch)
	}
	if diff != "" {
		t.Errorf("expected empty diff, got %q", diff)
	}
}

func TestComputeYAMLPatch_OmittedResourceRequirementClassIDIsNotDrift(t *testing.T) {
	local := &PulledApp{App: "my-app", Replicas: 1}
	server := map[string]any{
		"name":                       "my-app",
		"replicas":                   float64(1),
		"resourceRequirementClassId": "app.sl1.beff",
	}
	patch, diff, _, _ := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch when local omits resourceRequirementClassId (preserve), got %+v", patch)
	}
	if diff != "" {
		t.Errorf("expected empty diff, got %q", diff)
	}
}

func TestComputeYAMLPatch_OmittedClusterDomainIDIsNotDrift(t *testing.T) {
	local := &PulledApp{App: "my-app", Replicas: 1}
	server := map[string]any{
		"name":            "my-app",
		"replicas":        float64(1),
		"clusterDomainId": "cd-abc12",
	}
	patch, diff, _, _ := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch when local omits clusterDomainId (preserve), got %+v", patch)
	}
	if diff != "" {
		t.Errorf("expected empty diff, got %q", diff)
	}
}

// Sanity: when local does have a non-zero value that disagrees with the
// server, drift IS reported (the gate doesn't over-suppress).
func TestComputeYAMLPatch_ExplicitReplicasMismatchIsDrift(t *testing.T) {
	local := &PulledApp{App: "my-app", Replicas: 3}
	server := map[string]any{
		"name":     "my-app",
		"replicas": float64(1),
	}
	patch, diff, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when local replicas differ from server")
	}
	if patch["replicas"] != 3 {
		t.Errorf("patch should set replicas to local value 3, got %+v", patch["replicas"])
	}
	if diff == "" {
		t.Error("expected non-empty diff so the user sees the replica change")
	}
}

// Thin yaml on a named RRC, no resource overrides anywhere: no
// promotion. Sanity check that the new client-side flip detection
// doesn't false-positive on the canonical pulled-yaml shape.
func TestComputeYAMLPatch_NamedRRCNoOverridesNoPromotion(t *testing.T) {
	local := &PulledApp{
		App:                        "my-app",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		ResourceRequirementClassID: "app.sl1.beff",
	}
	server := map[string]any{
		"name":                       "my-app",
		"resourceRequirementClassId": "app.sl1.beff",
		"replicas":                   float64(1),
		"cpuRequestMc":               float64(50),
		"cpuLimitMc":                 float64(1000),
		"memoryRequestMb":            float64(128),
		"memoryLimitMb":              float64(2048),
	}
	patch, _, _, promotion := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch (no drift on a clean named-RRC pull), got %+v", patch)
	}
	if promotion != "" {
		t.Errorf("expected no promotion notice, got %q", promotion)
	}
}

// Named RRC with an explicit Replicas value that MATCHES the server
// snapshot's class default: no flip. The local yaml is redundant (the
// class already bakes the count in) but it's not a conflict. We don't
// want to flip apps that round-trip cleanly just because the user
// happened to spell out the default value.
func TestComputeYAMLPatch_NamedRRCMatchingReplicasNoPromotion(t *testing.T) {
	local := &PulledApp{
		App:                        "my-app",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		Replicas:                   1, // matches the class default below
		ResourceRequirementClassID: "app.sl1.beff",
	}
	server := map[string]any{
		"name":                       "my-app",
		"resourceRequirementClassId": "app.sl1.beff",
		"replicas":                   float64(1),
	}
	_, _, _, promotion := computeYAMLPatch(local, server, nil)
	if promotion != "" {
		t.Errorf("matching replicas should not trigger a flip, got promotion=%q", promotion)
	}
}

// Named RRC + conflicting Replicas: the flip fires. The wire body
// carries rrcId=custom (not the named class) and backfills cpu/memory
// from the server snapshot, because the thin local yaml didn't carry
// them and the server's stored values for this class are what the
// post-flip "custom" state must inherit. Without the backfill the
// PATCH would be a partial custom payload and the conductor would
// reject (or worse, resolve cpu/memory to zero-init defaults).
func TestComputeYAMLPatch_NamedRRCConflictingReplicasFlipsToCustom(t *testing.T) {
	local := &PulledApp{
		App:                        "my-app",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		Replicas:                   2, // user-added override, conflicts with sl1.beff default
		ResourceRequirementClassID: "app.sl1.beff",
	}
	server := map[string]any{
		"name":                       "my-app",
		"resourceRequirementClassId": "app.sl1.beff",
		"replicas":                   float64(1),
		"cpuRequestMc":               float64(50),
		"cpuLimitMc":                 float64(1000),
		"memoryRequestMb":            float64(128),
		"memoryLimitMb":              float64(2048),
	}
	patch, _, _, promotion := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when replicas conflicts with named-RRC default")
	}
	if got := patch["resourceRequirementClassId"]; got != "custom" {
		t.Errorf("rrcId should flip to custom on the wire, got %v", got)
	}
	if got := patch["replicas"]; got != 2 {
		t.Errorf("replicas should carry the user override (2), got %v", got)
	}
	// Backfill from the snapshot: every resource dimension must reach
	// the wire so the post-flip custom record is complete.
	for k, want := range map[string]int{
		"cpuRequestMc":    50,
		"cpuLimitMc":      1000,
		"memoryRequestMb": 128,
		"memoryLimitMb":   2048,
	} {
		got, ok := patch[k]
		if !ok {
			t.Errorf("patch missing backfilled %q (need %d so custom payload is complete)", k, want)
			continue
		}
		if got != want {
			t.Errorf("patch[%q] = %v, want %d (from server snapshot)", k, got, want)
		}
	}
	if promotion == "" {
		t.Error("expected promotion notice describing the flip and the backfill")
	}
	if !strings.Contains(promotion, "app.sl1.beff") || !strings.Contains(promotion, "custom") {
		t.Errorf("promotion notice should name the source class and the destination; got %q", promotion)
	}
}

// Named RRC + conflicting cpuLimit (via a pointer override on the
// pulled-app struct). Same flip path as replicas; verifies the cpu
// dimension is wired into the conflict detector. Useful canary in
// case someone refactors promoteToCustomIfConflicted and accidentally
// special-cases the Replicas field.
func TestComputeYAMLPatch_NamedRRCConflictingCPUFlipsToCustom(t *testing.T) {
	custom := 1500
	local := &PulledApp{
		App:                        "my-app",
		ID:                         "ab12c",
		CID:                        "k1",
		AID:                        "acc-1",
		CPULimitMc:                 &custom, // overrides sl1.beff's 1000
		ResourceRequirementClassID: "app.sl1.beff",
	}
	server := map[string]any{
		"name":                       "my-app",
		"resourceRequirementClassId": "app.sl1.beff",
		"replicas":                   float64(1),
		"cpuRequestMc":               float64(50),
		"cpuLimitMc":                 float64(1000),
		"memoryRequestMb":            float64(128),
		"memoryLimitMb":              float64(2048),
	}
	patch, _, _, promotion := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when cpuLimitMc conflicts with named-RRC default")
	}
	if got := patch["resourceRequirementClassId"]; got != "custom" {
		t.Errorf("rrcId should flip to custom, got %v", got)
	}
	if got := patch["cpuLimitMc"]; got != 1500 {
		t.Errorf("cpuLimitMc should carry the user override (1500), got %v", got)
	}
	if got := patch["replicas"]; got != 1 {
		t.Errorf("replicas should be backfilled to the class default (1), got %v", got)
	}
	if promotion == "" {
		t.Error("expected promotion notice")
	}
}

func TestComputeYAMLPatch_HealthCheckClearedByOmission(t *testing.T) {
	// Local omits healthCheck; server has healthCheck=tcp. Under the
	// new conductor semantics this is "user wants healthCheck cleared",
	// which sync surfaces as drift and the wire body intentionally
	// omits the field so the server applies omit-equals-clear.
	local := &PulledApp{
		App: "my-app", Replicas: 1,
	}
	server := map[string]any{
		"name":            "my-app",
		"replicas":        float64(1),
		"healthCheck":     "tcp",
		"healthCheckPort": float64(3000),
		"healthCheckPath": "/healthz",
	}
	patch, diff, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch (server has health-check, local doesn't)")
	}
	for _, k := range []string{"healthCheck", "healthCheckPort", "healthCheckPath"} {
		if _, ok := patch[k]; ok {
			t.Errorf("body must omit %q so the server clears it, got %+v", k, patch)
		}
	}
	if diff == "" {
		t.Error("expected non-empty diff so the user sees what's about to clear")
	}
}

func TestComputeYAMLPatch_PortsTranslateStandardHttps(t *testing.T) {
	local := &PulledApp{
		Replicas:            1,
		ServicePortMappings: []Port{{Port: 8080, StandardHttps: false}},
	}
	server := map[string]any{
		"replicas": float64(1),
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}
	patch, _, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected patch")
	}
	mappings, ok := patch["servicePortMappings"].([]map[string]any)
	if !ok {
		t.Fatalf("servicePortMappings type: %T", patch["servicePortMappings"])
	}
	if mappings[0]["standardHttps"] != false {
		t.Errorf("expected standardHttps=false, got %+v", mappings[0])
	}
}

// TestPortsDiffer_DomainProxiedFlip ensures a per-domain proxied flip on the
// local side surfaces as drift even when fqdn + port match the server.
func TestPortsDiffer_DomainProxiedFlip(t *testing.T) {
	local := []Port{{
		Port: 8080, StandardHttps: true,
		Domains: []MappingDomain{{Fqdn: "app.example.com", EnableCloudflareProxy: true}},
	}}
	server := []any{
		map[string]any{
			"port":          float64(8080),
			"standardHttps": true,
			"domains": []any{
				map[string]any{"fqdn": "app.example.com", "enableCloudflareProxy": false},
			},
		},
	}
	if !portsDiffer(local, server) {
		t.Error("expected drift when proxied flips")
	}
}

// TestPortsDiffer_DomainOrderingNoDrift ensures yaml ordering of domains
// doesn't trigger a false positive (set-equal compare).
func TestPortsDiffer_DomainOrderingNoDrift(t *testing.T) {
	local := []Port{{
		Port: 8080, StandardHttps: true,
		Domains: []MappingDomain{
			{Fqdn: "b.example.com", EnableCloudflareProxy: false},
			{Fqdn: "a.example.com", EnableCloudflareProxy: true},
		},
	}}
	server := []any{
		map[string]any{
			"port":          float64(8080),
			"standardHttps": true,
			"domains": []any{
				map[string]any{"fqdn": "a.example.com", "enableCloudflareProxy": true},
				map[string]any{"fqdn": "b.example.com", "enableCloudflareProxy": false},
			},
		},
	}
	if portsDiffer(local, server) {
		t.Error("expected no drift when only ordering differs")
	}
}

// TestComputeYAMLPatch_DomainsCarryProxied ensures the PATCH payload sends
// `domains: [{fqdn, proxied}]` per mapping (not bare fqdn strings).
func TestComputeYAMLPatch_DomainsCarryProxied(t *testing.T) {
	local := &PulledApp{
		Replicas: 1,
		ServicePortMappings: []Port{{
			Port: 8080, StandardHttps: true,
			Domains: []MappingDomain{{Fqdn: "app.example.com", EnableCloudflareProxy: true}},
		}},
	}
	server := map[string]any{
		"replicas": float64(1),
		// server has no domains for the port → drift
		"servicePortMappings": []any{
			map[string]any{"port": float64(8080), "standardHttps": true},
		},
	}
	patch, _, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected patch when domains added locally")
	}
	mappings, ok := patch["servicePortMappings"].([]map[string]any)
	if !ok {
		t.Fatalf("servicePortMappings type: %T", patch["servicePortMappings"])
	}
	domains, ok := mappings[0]["domains"].([]map[string]any)
	if !ok {
		t.Fatalf("domains type: %T", mappings[0]["domains"])
	}
	if len(domains) != 1 || domains[0]["fqdn"] != "app.example.com" || domains[0]["enableCloudflareProxy"] != true {
		t.Errorf("expected [{fqdn: app.example.com, proxied: true}], got %+v", domains)
	}
}

func TestComputeYAMLPatch_VCSChangeBecomesRefused(t *testing.T) {
	local := &PulledApp{
		Replicas: 1, DeployType: "vcs",
		Integration: &Integration{ID: "tr6mj", RepoID: 1, RepoName: "x/y", BranchName: "develop"},
	}
	server := map[string]any{
		"replicas":         float64(1),
		"deployType":       "vcs",
		"integrationType":  "gitlab-runner",
		"vcsIntegrationId": "tr6mj",
		"repoId":           float64(1),
		"repoName":         "x/y",
		"branchName":       "main", // user changed locally
	}
	_, _, refused, _ := computeYAMLPatch(local, server, nil)
	if len(refused) == 0 {
		t.Error("expected vcs change to be refused")
	}
}

// V13: sourceDir / dockerfile drift detection. Both are partial-update
// fields like clusterDomainId (omit = preserve), so only a non-empty
// local that disagrees with the server qualifies as drift. Bug this
// guards: pre-V13, neither field round-tripped via the AppDocument, so
// monorepo apps lost sourceDir on every fresh-checkout pull and the
// next build failed at "failed to read dockerfile."
func TestComputeYAMLPatch_SourceDirDriftIsDetected(t *testing.T) {
	local := &PulledApp{App: "web", Replicas: 1, SourceDir: "../../../apps/backend"}
	server := map[string]any{
		"name":     "web",
		"replicas": float64(1),
		// server has no sourceDir → drift
	}
	patch, _, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when local sourceDir is set and server has none")
	}
	if patch["sourceDir"] != "../../../apps/backend" {
		t.Errorf("patch must carry local sourceDir; got %+v", patch["sourceDir"])
	}
}

func TestComputeYAMLPatch_DockerfileDriftIsDetected(t *testing.T) {
	local := &PulledApp{App: "web", Replicas: 1, Dockerfile: "docker/prod.Dockerfile"}
	server := map[string]any{
		"name":       "web",
		"replicas":   float64(1),
		"dockerfile": "Dockerfile", // server has different value
	}
	patch, _, _, _ := computeYAMLPatch(local, server, nil)
	if patch == nil {
		t.Fatal("expected non-nil patch when local dockerfile differs from server")
	}
	if patch["dockerfile"] != "docker/prod.Dockerfile" {
		t.Errorf("patch must carry local dockerfile; got %+v", patch["dockerfile"])
	}
}

func TestComputeYAMLPatch_OmittedSourceDirIsNotDrift(t *testing.T) {
	// Local yaml doesn't set sourceDir; server does. Empty-on-local means
	// "preserve server value", so the diff engine must NOT report drift
	// (otherwise every existing yaml that doesn't set the field would
	// noisily replan on every sync).
	local := &PulledApp{App: "web", Replicas: 1}
	server := map[string]any{
		"name":      "web",
		"replicas":  float64(1),
		"sourceDir": "../../../apps/backend",
	}
	patch, _, _, _ := computeYAMLPatch(local, server, nil)
	if patch != nil {
		t.Errorf("expected nil patch when local omits sourceDir (preserve); got %+v", patch)
	}
}

func TestBuildPulledApp_ExtractsBuildMetadata(t *testing.T) {
	// V13: BuildPulledApp must lift sourceDir and dockerfile from the
	// raw apps/:id response so apps_pull writes them back into a fresh
	// checkout. Pre-V13 these fields didn't round-trip; the regression
	// is silent yaml truncation followed by a build failure.
	raw := map[string]any{
		"id":         "qu5db",
		"name":       "aliens-frontend-dev",
		"replicas":   float64(1),
		"sourceDir":  "../../../apps/frontend",
		"dockerfile": "Dockerfile",
	}
	got := BuildPulledApp(raw, "mycluster2", "myacct")
	if got.SourceDir != "../../../apps/frontend" {
		t.Errorf("SourceDir: got %q, want ../../../apps/frontend", got.SourceDir)
	}
	if got.Dockerfile != "Dockerfile" {
		t.Errorf("Dockerfile: got %q, want Dockerfile", got.Dockerfile)
	}
}

func TestBuildPulledApp_OmitsBuildMetadataWhenServerHasNone(t *testing.T) {
	// Single-app-at-root projects shouldn't get noisy yamls. When the
	// server doesn't carry these fields, the PulledApp leaves them
	// empty so omitempty drops them from the marshaled yaml.
	raw := map[string]any{
		"id":       "appid9",
		"name":     "aliens-backend-dev",
		"replicas": float64(1),
	}
	got := BuildPulledApp(raw, "mycluster2", "myacct")
	if got.SourceDir != "" {
		t.Errorf("SourceDir should be empty when server omits it; got %q", got.SourceDir)
	}
	if got.Dockerfile != "" {
		t.Errorf("Dockerfile should be empty when server omits it; got %q", got.Dockerfile)
	}
}

// ---------------------------------------------------------------------------
// LoadLocalEnv / LoadLocalSecretFiles
// ---------------------------------------------------------------------------

func TestLoadLocalEnv_ParsesKeyValueLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my.env")
	if err := os.WriteFile(path, []byte("A=1\nB=two=parts\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, exists, err := LoadLocalEnv(dir, "my.env", "")
	if err != nil {
		t.Fatalf("LoadLocalEnv: %v", err)
	}
	if !exists {
		t.Error("exists should be true")
	}
	if got["A"] != "1" || got["B"] != "two=parts" {
		t.Errorf("parsed wrong: %+v", got)
	}
}

func TestLoadLocalSecretFiles_FollowsYAMLManifest(t *testing.T) {
	// New contract: the local yaml's secretFiles[].local entries are the
	// manifest. LoadLocalSecretFiles reads exactly those, by filename.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha-file"), []byte("alpha"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta-file"), []byte("beta"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// A file present on disk but not declared in the yaml must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "stray-file"), []byte("ignored"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := []SecretFile{
		{Filename: "a", MountPath: "/a", Local: "alpha-file"},
		{Filename: "b", MountPath: "/b", Local: "beta-file"},
	}
	got, err := LoadLocalSecretFiles(dir, files)
	if err != nil {
		t.Fatalf("LoadLocalSecretFiles: %v", err)
	}
	if string(got["a"]) != "alpha" || string(got["b"]) != "beta" {
		t.Errorf("unexpected content: %+v", got)
	}
	if _, present := got["stray-file"]; present {
		t.Errorf("undeclared file should not be loaded: %+v", got)
	}
}

func TestLoadLocalEnv_ReadsRefFromYAML(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "custom-env-name")
	if err := os.WriteFile(envPath, []byte("FOO=bar\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, exists, err := LoadLocalEnv(dir, "custom-env-name", "")
	if err != nil {
		t.Fatalf("LoadLocalEnv: %v", err)
	}
	if !exists {
		t.Error("exists should be true")
	}
	if got["FOO"] != "bar" {
		t.Errorf("FOO: got %q, want bar", got["FOO"])
	}
}

func TestLoadLocalEnv_NoRefAndNoDefaultMeansNoEnv(t *testing.T) {
	got, exists, err := LoadLocalEnv(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("LoadLocalEnv: %v", err)
	}
	if exists {
		t.Error("exists should be false when no env ref in yaml and no default path provided")
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// Regression test for V3 (VCS_DEPLOY_TEST_NOTES.md): when the yaml omits
// the env ref but a file exists at the documented default path, LoadLocalEnv
// must fall back to that default. Pre-fix: ref="" silently returned empty
// without checking the default path, causing apps_sync to skip pushing
// the file's content (silent data loss in the fresh-checkout flow).
func TestLoadLocalEnv_FallsBackToDefaultPathWhenRefEmpty(t *testing.T) {
	dir := t.TempDir()
	defaultLeaf := EnvFilename("k1", "ab12c") // canonical default
	if err := os.WriteFile(filepath.Join(dir, defaultLeaf), []byte("APP_NAME=aliens\nLOG_LEVEL=debug\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, exists, err := LoadLocalEnv(dir, "", defaultLeaf)
	if err != nil {
		t.Fatalf("LoadLocalEnv: %v", err)
	}
	if !exists {
		t.Error("exists should be true when default-path file is present")
	}
	if got["APP_NAME"] != "aliens" || got["LOG_LEVEL"] != "debug" {
		t.Errorf("content wrong: %+v", got)
	}
}

// Companion test: yaml ref takes precedence over the default fallback so
// users who explicitly set a custom path are never overridden.
func TestLoadLocalEnv_RefSetWinsOverDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ref.env"), []byte("FROM=ref\n"), 0600); err != nil {
		t.Fatalf("write ref: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "default.env"), []byte("FROM=default\n"), 0600); err != nil {
		t.Fatalf("write default: %v", err)
	}
	got, exists, err := LoadLocalEnv(dir, "ref.env", "default.env")
	if err != nil {
		t.Fatalf("LoadLocalEnv: %v", err)
	}
	if !exists {
		t.Error("exists should be true when ref file is present")
	}
	if got["FROM"] != "ref" {
		t.Errorf("ref must win over default; got FROM=%q", got["FROM"])
	}
}

// ---------------------------------------------------------------------------
// SecretFilesChange.AllAddPayloads
// ---------------------------------------------------------------------------

func TestAllAddPayloads_MergesAddBeforeUpdate(t *testing.T) {
	change := &SecretFilesChange{
		Add:    []SecretFilePayload{{Filename: "new.crt"}},
		Update: []SecretFilePayload{{Filename: "drift.crt"}},
		Remove: []string{"gone.crt"},
	}
	got := change.AllAddPayloads()
	if len(got) != 2 {
		t.Fatalf("expected 2 payloads, got %d: %+v", len(got), got)
	}
	// Order matters: server's `add` array is positional; adds come first
	// so brand-new files don't get overwritten by an update with the same
	// filename in the same payload.
	if got[0].Filename != "new.crt" || got[1].Filename != "drift.crt" {
		t.Errorf("unexpected order: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// LoadLocalApp
// ---------------------------------------------------------------------------

func TestLoadLocalApp_ParsesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runos.yaml")
	yaml := `app: my-app
deployType: cli
id: ab12c
cid: k1
aid: acc-1
replicas: 3
servicePortMappings:
  - port: 8080
    standardHttps: true
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadLocalApp(path)
	if err != nil {
		t.Fatalf("LoadLocalApp: %v", err)
	}
	if got.App != "my-app" || got.ID != "ab12c" || got.CID != "k1" || got.AID != "acc-1" || got.Replicas != 3 {
		t.Errorf("unexpected app: %+v", got)
	}
	if len(got.ServicePortMappings) != 1 || got.ServicePortMappings[0].Port != 8080 {
		t.Errorf("ports: %+v", got.ServicePortMappings)
	}
}

func TestLoadLocalApp_MissingFile(t *testing.T) {
	_, err := LoadLocalApp(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadLocalApp_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("app: [unterminated\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadLocalApp(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	// Error must mention the file so users know which yaml is bad.
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("expected error to name the file, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadLocalOverrides
// ---------------------------------------------------------------------------

func TestLoadLocalOverrides_ReadsBodies(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("body-a"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte("body-b"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	overrides := []Override{
		{ID: "id-a", Name: "first", Enabled: true, Local: "a.yaml"},
		{ID: "", Name: "second", Enabled: false, Local: "b.yaml"},
	}
	got, err := LoadLocalOverrides(dir, overrides)
	if err != nil {
		t.Fatalf("LoadLocalOverrides: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %d: %+v", len(got), got)
	}
	if got[0].ID != "id-a" || got[0].Name != "first" || !got[0].Enabled || string(got[0].Content) != "body-a" {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].ID != "" || got[1].Name != "second" || got[1].Enabled || string(got[1].Content) != "body-b" {
		t.Errorf("second: %+v", got[1])
	}
}

func TestLoadLocalOverrides_MissingLocalPath(t *testing.T) {
	_, err := LoadLocalOverrides(t.TempDir(), []Override{{Name: "no-path"}})
	if err == nil {
		t.Fatal("expected error when local path is empty")
	}
	if !strings.Contains(err.Error(), "no-path") {
		t.Errorf("expected error to name the override, got: %v", err)
	}
}

func TestLoadLocalOverrides_MissingFile(t *testing.T) {
	_, err := LoadLocalOverrides(t.TempDir(), []Override{{Name: "ghost", Local: "ghost.yaml"}})
	if err == nil {
		t.Fatal("expected error when referenced file is missing")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected error to name the override, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ComputeSyncPlan (end-to-end orchestration)
// ---------------------------------------------------------------------------

func TestComputeSyncPlan_AggregatesAllSections(t *testing.T) {
	in := SyncInputs{
		LocalApp: &PulledApp{
			App:                 "my-app",
			ID:                  "ab12c",
			CID:                 "k1",
			AID:                 "acc-1",
			Replicas:            3, // changed
			ServicePortMappings: []Port{{Port: 8080, StandardHttps: true}},
		},
		LocalEnvVars:     map[string]string{"NEW": "yes", "SAME": "ok"},
		LocalSecretFiles: map[string][]byte{"new.pem": []byte("brand-new\n")},
		LocalOverrides: []LocalOverride{
			{ID: "", Name: "fresh-override", Enabled: true, Content: []byte("body")},
		},

		ServerRaw: map[string]any{
			"name":     "my-app",
			"replicas": float64(1),
			"servicePortMappings": []any{
				map[string]any{"port": float64(8080), "standardHttps": true},
			},
		},
		ServerEnvVars:     map[string]string{"SAME": "ok", "STALE": "x"},
		ServerSecretFiles: []SecretFileSummary{{Filename: "gone.pem", MountPath: "/gone.pem", MD5: "abc"}},
		ServerOverrides:   nil,
	}

	plan := ComputeSyncPlan(in)
	if plan == nil {
		t.Fatal("expected plan")
	}
	if plan.AppID != "ab12c" || plan.AppName != "my-app" || plan.CID != "k1" {
		t.Errorf("identity: %+v", plan)
	}
	if !plan.HasChanges() {
		t.Fatal("expected HasChanges to be true")
	}
	if _, ok := plan.YAMLPatch["replicas"]; !ok {
		t.Errorf("expected replicas patch, got %+v", plan.YAMLPatch)
	}
	if plan.Env == nil || plan.Env.Add["NEW"] != "yes" {
		t.Errorf("env add: %+v", plan.Env)
	}
	if plan.Env == nil || len(plan.Env.Remove) != 1 || plan.Env.Remove[0] != "STALE" {
		t.Errorf("env remove: %+v", plan.Env)
	}
	if plan.SecretFiles == nil || len(plan.SecretFiles.Add) != 1 || plan.SecretFiles.Add[0].Filename != "new.pem" {
		t.Errorf("secret-files add: %+v", plan.SecretFiles)
	}
	if plan.SecretFiles == nil || len(plan.SecretFiles.Remove) != 1 || plan.SecretFiles.Remove[0] != "gone.pem" {
		t.Errorf("secret-files remove: %+v", plan.SecretFiles)
	}
	if len(plan.Overrides) != 1 || plan.Overrides[0].Op != "add" || plan.Overrides[0].Name != "fresh-override" {
		t.Errorf("overrides: %+v", plan.Overrides)
	}
}

func TestComputeSyncPlan_NoChangesProducesEmptyPlan(t *testing.T) {
	in := SyncInputs{
		LocalApp: &PulledApp{
			App: "my-app", ID: "ab12c", CID: "k1", AID: "acc-1",
			Replicas:            1,
			ServicePortMappings: []Port{{Port: 8080, StandardHttps: true}},
		},
		LocalEnvVars:     map[string]string{"A": "1"},
		LocalSecretFiles: map[string][]byte{},

		ServerRaw: map[string]any{
			"name":     "my-app",
			"replicas": float64(1),
			"servicePortMappings": []any{
				map[string]any{"port": float64(8080), "standardHttps": true},
			},
		},
		ServerEnvVars:     map[string]string{"A": "1"},
		ServerSecretFiles: nil,
		ServerOverrides:   nil,
	}

	plan := ComputeSyncPlan(in)
	if plan == nil {
		t.Fatal("expected plan struct (always non-nil)")
	}
	if plan.HasChanges() {
		t.Errorf("expected no changes, got plan: %+v", plan)
	}
	// I4-I: HasChangesField mirrors HasChanges() and is always
	// emitted in JSON. Pre-fix, `apps_sync --dry-run --json` on a
	// no-op plan collapsed to {appId, appName, cid} and CI parsers
	// had no signal between "ran cleanly, nothing to do" and
	// "ran a partial plan we should investigate".
	if plan.HasChangesField {
		t.Errorf("HasChangesField must mirror HasChanges() (false here)")
	}
}

// I4-I: a plan with changes must have HasChangesField=true so JSON
// consumers can branch on a single field without having to inspect
// every section.
func TestComputeSyncPlan_HasChangesFieldTrueOnChanges(t *testing.T) {
	in := SyncInputs{
		LocalApp: &PulledApp{
			ID: "ab12c", CID: "k1", AID: "acc-1", App: "web",
			Replicas:            2,
			ServicePortMappings: []Port{{Port: 8080, StandardHttps: true}},
		},
		LocalEnvVars: map[string]string{"NEW_KEY": "value"},
		ServerRaw: map[string]any{
			"id":       "ab12c",
			"name":     "web",
			"replicas": float64(1), // diverges from local
			"servicePortMappings": []any{
				map[string]any{"port": float64(8080), "standardHttps": true},
			},
		},
	}
	plan := ComputeSyncPlan(in)
	if !plan.HasChanges() {
		t.Fatal("expected HasChanges() to be true given replicas + env delta")
	}
	if !plan.HasChangesField {
		t.Errorf("HasChangesField must mirror HasChanges() (true here)")
	}
}

// I4-I: HasChangesField must round-trip through JSON serialisation
// so consumers always see the field in the wire format.
func TestSyncPlan_HasChangesFieldJSONShape(t *testing.T) {
	plan := &SyncPlan{AppID: "ab12c", AppName: "web", CID: "k1"}
	// Simulate the no-op outcome.
	plan.HasChangesField = plan.HasChanges()
	out, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"hasChanges":false`) {
		t.Errorf("expected JSON to carry hasChanges:false, got %s", string(out))
	}
}

// ---------------------------------------------------------------------------
// requires drift refusal
// ---------------------------------------------------------------------------

func TestComputeSyncPlan_RequiresPatched(t *testing.T) {
	requiresEntry := func(plan *SyncPlan, alias string) (map[string]any, bool) {
		req, ok := plan.YAMLPatch["requires"].(map[string]any)
		if !ok {
			return nil, false
		}
		entry, ok := req[alias].(map[string]any)
		return entry, ok
	}

	t.Run("type/id mismatch produces requires patch", func(t *testing.T) {
		// Server's id drifted from what local says. Sync now PATCHes
		// the requires block server-side instead of refusing.
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {ID: "mjn1d", Type: "postgresql"},
				},
			},
			ServerRaw: map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{
				"poll-app-db": {Type: "postgresql", ID: "xY9zW"},
			},
		}
		plan := ComputeSyncPlan(in)
		entry, ok := requiresEntry(plan, "poll-app-db")
		if !ok {
			t.Fatalf("expected requires patch with poll-app-db; got %+v", plan.YAMLPatch)
		}
		if entry["id"] != "mjn1d" || entry["type"] != "postgresql" {
			t.Errorf("patch entry should carry local type/id; got %+v", entry)
		}
	})

	t.Run("config drift produces requires patch", func(t *testing.T) {
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {
						ID: "mjn1d", Type: "postgresql",
						Config: map[string]any{"databaseName": "pollapp"},
					},
				},
			},
			ServerRaw: map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{
				"poll-app-db": {
					Type: "postgresql", ID: "mjn1d",
					Config: map[string]any{"databaseName": "renamed"},
				},
			},
		}
		plan := ComputeSyncPlan(in)
		entry, ok := requiresEntry(plan, "poll-app-db")
		if !ok {
			t.Fatalf("expected requires patch; got %+v", plan.YAMLPatch)
		}
		cfg, _ := entry["config"].(map[string]any)
		if cfg["databaseName"] != "pollapp" {
			t.Errorf("patch should carry local config; got %+v", cfg)
		}
	})

	t.Run("legacy app: empty server config gets populated by sync push", func(t *testing.T) {
		// Apps deployed before /requires landed return empty
		// Config/Env. The migration path is "sync pushes local values
		// up", same wire surface as a regular drift fix. So this DOES
		// produce a patch (unlike the diff-display merge logic, which
		// suppresses the false drift).
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {
						ID: "mjn1d", Type: "postgresql",
						Config: map[string]any{"databaseName": "pollapp"},
						Env:    map[string]string{"url": "DATABASE_URL"},
					},
				},
			},
			ServerRaw: map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{
				"poll-app-db": {Type: "postgresql", ID: "mjn1d"},
			},
		}
		plan := ComputeSyncPlan(in)
		entry, ok := requiresEntry(plan, "poll-app-db")
		if !ok {
			t.Fatalf("legacy migration path should produce a patch; got %+v", plan.YAMLPatch)
		}
		cfg, _ := entry["config"].(map[string]any)
		if cfg["databaseName"] != "pollapp" {
			t.Errorf("patch should carry local config so legacy server fills in; got %+v", entry)
		}
	})

	t.Run("server has dep local doesn't: patch removes via full replacement", func(t *testing.T) {
		// Local has no requires; server has poll-app-cache. Patching
		// with an empty requires block tells the server to drop the
		// link (full replacement: omitted aliases removed).
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
			},
			ServerRaw: map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{
				"poll-app-cache": {Type: "valkey", ID: "xY9zW"},
			},
		}
		plan := ComputeSyncPlan(in)
		req, ok := plan.YAMLPatch["requires"].(map[string]any)
		if !ok {
			t.Fatalf("expected requires patch; got %+v", plan.YAMLPatch)
		}
		if len(req) != 0 {
			t.Errorf("patch should be empty map (drops the alias); got %+v", req)
		}
	})

	t.Run("local has dep server doesn't: patch adds it", func(t *testing.T) {
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {ID: "mjn1d", Type: "postgresql"},
				},
			},
			ServerRaw:      map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{},
		}
		plan := ComputeSyncPlan(in)
		entry, ok := requiresEntry(plan, "poll-app-db")
		if !ok {
			t.Fatalf("expected requires patch with poll-app-db; got %+v", plan.YAMLPatch)
		}
		if entry["type"] != "postgresql" {
			t.Errorf("patch should carry local type; got %+v", entry)
		}
	})

	t.Run("class is stripped from patch payload", func(t *testing.T) {
		// PATCH /apps/:id rejects class on requires entries (sizing is
		// a service-side concern). Make sure we never send it on the
		// wire even when the local yaml has it set.
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {
						ID: "mjn1d", Type: "postgresql",
						Class: "postgresql.c0.tiny",
					},
				},
			},
			ServerRaw:      map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{},
		}
		plan := ComputeSyncPlan(in)
		entry, ok := requiresEntry(plan, "poll-app-db")
		if !ok {
			t.Fatalf("expected requires patch; got %+v", plan.YAMLPatch)
		}
		if _, hasClass := entry["class"]; hasClass {
			t.Errorf("class must not be sent on wire (PATCH endpoint rejects it); got %+v", entry)
		}
	})

	t.Run("requires in sync produces no patch", func(t *testing.T) {
		// Class differs (local has it, server doesn't return it) but
		// class is excluded from the comparison; everything else
		// matches → no drift → patch should be nil entirely.
		in := SyncInputs{
			LocalApp: &PulledApp{
				App: "web", ID: "ab12c", CID: "k1", AID: "acc-1",
				Requires: map[string]ServiceRequirement{
					"poll-app-db": {
						ID: "mjn1d", Type: "postgresql",
						Class:  "postgresql.c0.tiny",
						Config: map[string]any{"databaseName": "pollapp"},
						Env:    map[string]string{"url": "DATABASE_URL"},
					},
				},
			},
			ServerRaw: map[string]any{"name": "web"},
			ServerRequires: map[string]ServiceRequirement{
				"poll-app-db": {
					Type: "postgresql", ID: "mjn1d",
					Config: map[string]any{"databaseName": "pollapp"},
					Env:    map[string]string{"url": "DATABASE_URL"},
				},
			},
		}
		plan := ComputeSyncPlan(in)
		if plan.YAMLPatch != nil {
			t.Errorf("expected nil YAMLPatch when nothing drifts; got %+v", plan.YAMLPatch)
		}
	})
}

// ---------------------------------------------------------------------------
// CheckEmptySecretEnvWipe
// ---------------------------------------------------------------------------

// Round 5 T1: an `apps_sync` against an app where the local secret-env
// file was missing or empty silently sent `{ envVars: {} }` to the
// replace-all endpoint, wiping every server secret. This footgun bites
// hardest on fresh checkouts (gitignored env files don't come down with
// the repo). The gate refuses the destructive case unless the user
// explicitly opts in via --allow-empty-secret-env.

func TestCheckEmptySecretEnvWipe_RefusesEmptyReplaceWithServerKeys(t *testing.T) {
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final:  map[string]string{},
			Remove: []string{"DATABASE_PASS", "STRIPE_KEY", "JWT_SECRET"},
		},
	}
	err := CheckEmptySecretEnvWipe(plan /* allowEmpty */, false, nil)
	if err == nil {
		t.Fatal("expected refusal when local is empty and server has keys")
	}
	msg := err.Error()
	// Error must name the count and the keys, otherwise the user can't
	// tell what they're about to lose.
	if !strings.Contains(msg, "3") {
		t.Errorf("error should name the key count, got: %s", msg)
	}
	for _, k := range []string{"DATABASE_PASS", "STRIPE_KEY", "JWT_SECRET"} {
		if !strings.Contains(msg, k) {
			t.Errorf("error should name key %q, got: %s", k, msg)
		}
	}
	// And point at the recovery flag.
	if !strings.Contains(msg, "--allow-empty-secret-env") {
		t.Errorf("error should mention --allow-empty-secret-env, got: %s", msg)
	}
}

func TestCheckEmptySecretEnvWipe_AllowsWhenOptedIn(t *testing.T) {
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final:  map[string]string{},
			Remove: []string{"A", "B"},
		},
	}
	err := CheckEmptySecretEnvWipe(plan /* allowEmpty */, true, nil)
	if err != nil {
		t.Errorf("expected pass with allowEmpty=true, got: %v", err)
	}
}

func TestCheckEmptySecretEnvWipe_PassesWhenLocalIsNonEmpty(t *testing.T) {
	// Local file has content; we're updating, not wiping. No gate.
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final:  map[string]string{"DATABASE_PASS": "new"},
			Update: map[string]string{"DATABASE_PASS": "new"},
		},
	}
	if err := CheckEmptySecretEnvWipe(plan, false, nil); err != nil {
		t.Errorf("expected pass with non-empty Final, got: %v", err)
	}
}

func TestCheckEmptySecretEnvWipe_PassesWhenServerHasNoKeys(t *testing.T) {
	// Local is empty AND server is empty — nothing to wipe, nothing to gate.
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final:  map[string]string{},
			Remove: nil,
		},
	}
	if err := CheckEmptySecretEnvWipe(plan, false, nil); err != nil {
		t.Errorf("expected pass when nothing to remove, got: %v", err)
	}
}

func TestCheckEmptySecretEnvWipe_PassesWhenSecretEnvSectionAbsent(t *testing.T) {
	// Plan has yaml/env/overrides changes but no secret-env section.
	plan := &SyncPlan{SecretEnv: nil}
	if err := CheckEmptySecretEnvWipe(plan, false, nil); err != nil {
		t.Errorf("expected pass when SecretEnv is nil, got: %v", err)
	}
}

func TestCheckEmptySecretEnvWipe_PreservedByPlatformDoesNotTriggerGate(t *testing.T) {
	// Local file is empty, server has only platform-injected keys (e.g.
	// DATABASE_URL from a requires alias). PreservedByPlatform is set,
	// Remove is empty → no wipe, no gate. Those keys will be re-injected
	// on next push regardless.
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final:               map[string]string{},
			Remove:              nil,
			PreservedByPlatform: []string{"DATABASE_URL", "CACHE_URL"},
		},
	}
	if err := CheckEmptySecretEnvWipe(plan, false, nil); err != nil {
		t.Errorf("expected pass when only PreservedByPlatform keys are present, got: %v", err)
	}
}

// Round 6 follow-up to R5 T1: the byte-empty check let the silent wipe
// through when the local file contained ONLY platform-injected names
// (e.g. just `DATABASE_URL=...`). The wire-body Final isn't empty in
// that case, but the EFFECTIVE user push is — DATABASE_URL gets
// re-injected on every push, so its presence isn't user intent. Without
// the platform-filter, APP_KEY (a real user key on the server) would
// silently disappear.
func TestCheckEmptySecretEnvWipe_RefusesWhenFinalHasOnlyPlatformInjected(t *testing.T) {
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			// Final carries DATABASE_URL because it's in the local file,
			// but DATABASE_URL is platform-claimed and re-injected on
			// every push — it doesn't represent a user-authored key.
			Final:  map[string]string{"DATABASE_URL": "postgresql://hand-authored"},
			Remove: []string{"APP_KEY", "STRIPE_SECRET"},
		},
	}
	platformInjected := map[string]bool{"DATABASE_URL": true}
	err := CheckEmptySecretEnvWipe(plan /* allowEmpty */, false, platformInjected)
	if err == nil {
		t.Fatal("expected refusal when Final contains only platform-injected keys and server has user keys to remove")
	}
	msg := err.Error()
	for _, k := range []string{"APP_KEY", "STRIPE_SECRET"} {
		if !strings.Contains(msg, k) {
			t.Errorf("error should name key %q, got: %s", k, msg)
		}
	}
	// And the message should now mention the broader trigger.
	if !strings.Contains(msg, "platform-injected") {
		t.Errorf("error should mention platform-injected names, got: %s", msg)
	}
}

func TestCheckEmptySecretEnvWipe_PassesWhenFinalMixesUserAndPlatform(t *testing.T) {
	// Mixed local file: APP_KEY (user) + DATABASE_URL (platform-injected).
	// The user-side push isn't empty (APP_KEY is real intent), so this
	// is a normal sync. No gate.
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Final: map[string]string{
				"DATABASE_URL": "postgresql://x",
				"APP_KEY":      "user-value",
			},
			Update: map[string]string{"APP_KEY": "user-value"},
		},
	}
	platformInjected := map[string]bool{"DATABASE_URL": true}
	if err := CheckEmptySecretEnvWipe(plan, false, platformInjected); err != nil {
		t.Errorf("expected pass when Final contains at least one user-set key, got: %v", err)
	}
}

// TestSyncPlan_RedactSecrets pins the JSON-path redaction added for
// I10-M: --redact-secrets must apply when the plan is emitted as JSON,
// not just when it's printed as text. Pre-fix `apps sync --dry-run
// --json --redact-secrets` leaked ADMIN_TOKEN / JWT_SECRET / full
// DATABASE_URL (with password) into LLM context via the MCP wrapper.
func TestSyncPlan_RedactSecrets(t *testing.T) {
	t.Parallel()
	plan := &SyncPlan{
		SecretEnv: &EnvChange{
			Add:    map[string]string{"ADMIN_TOKEN": "supersecret123"},
			Update: map[string]string{"JWT_SECRET": "jw7-secret"},
			Final: map[string]string{
				"ADMIN_TOKEN":  "supersecret123",
				"JWT_SECRET":   "jw7-secret",
				"DATABASE_URL": "postgresql://u:realpw@host:5432/db",
			},
		},
		Env: &EnvChange{
			Add:   map[string]string{"FEATURE_FLAG": "on"},
			Final: map[string]string{"FEATURE_FLAG": "on", "LOG_LEVEL": "debug"},
		},
	}
	plan.RedactSecrets()

	if plan.SecretEnv.Add["ADMIN_TOKEN"] != "<redacted>" {
		t.Errorf("ADMIN_TOKEN not redacted in Add: %q", plan.SecretEnv.Add["ADMIN_TOKEN"])
	}
	if plan.SecretEnv.Update["JWT_SECRET"] != "<redacted>" {
		t.Errorf("JWT_SECRET not redacted in Update: %q", plan.SecretEnv.Update["JWT_SECRET"])
	}
	for k, v := range plan.SecretEnv.Final {
		if v != "<redacted>" {
			t.Errorf("SecretEnv.Final[%q] = %q, want <redacted>", k, v)
		}
	}
	for k, v := range plan.Env.Final {
		if v != "<redacted>" {
			t.Errorf("Env.Final[%q] = %q, want <redacted> (plain env values also masked per --redact-secrets contract)", k, v)
		}
	}
}

func TestSyncPlan_RedactSecrets_NilSafe(t *testing.T) {
	t.Parallel()
	var plan *SyncPlan
	plan.RedactSecrets() // must not panic

	plan = &SyncPlan{}   // no SecretEnv, no Env
	plan.RedactSecrets() // must not panic
}

// TestRenderYAMLPatchAsDiff_StructuredValuesUseYAML pins I11-G: non-scalar
// values in the yamlDiff render as nested YAML, not Go `map[...]`
// literals. The diff is what `apps sync --dry-run --json` returns under
// `yamlDiff`; consumers need readable YAML so they can display or parse
// the change without writing a Go-map-literal grammar.
func TestRenderYAMLPatchAsDiff_StructuredValuesUseYAML(t *testing.T) {
	t.Parallel()

	patch := map[string]any{
		"servicePortMappings": []any{
			map[string]any{
				"port":          3000,
				"standardHttps": false,
				"domains": []any{
					map[string]any{
						"fqdn":                  "new.example.com",
						"enableCloudflareProxy": true,
					},
				},
			},
		},
	}
	server := map[string]any{
		"servicePortMappings": []any{
			map[string]any{
				"port":          3000,
				"standardHttps": false,
				"domains": []any{
					map[string]any{
						"fqdn":                  "old.example.com",
						"enableCloudflareProxy": false,
					},
				},
			},
		},
	}

	got := renderYAMLPatchAsDiff(patch, server)

	if strings.Contains(got, "map[") {
		t.Errorf("rendered diff must not contain Go map literals; got:\n%s", got)
	}
	if !strings.Contains(got, "- servicePortMappings:") {
		t.Errorf("expected '- servicePortMappings:' header line; got:\n%s", got)
	}
	if !strings.Contains(got, "+ servicePortMappings:") {
		t.Errorf("expected '+ servicePortMappings:' header line; got:\n%s", got)
	}
	if !strings.Contains(got, "new.example.com") || !strings.Contains(got, "old.example.com") {
		t.Errorf("expected both old/new fqdn values in diff; got:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if line == "" {
			continue
		}
		if line[0] != '-' && line[0] != '+' {
			t.Errorf("diff line missing -/+ prefix: %q (in:\n%s\n)", line, got)
		}
	}
}

// TestRenderYAMLPatchAsDiff_ScalarsStayOnOneLine pins the scalar
// rendering: scalars (string, bool, int) stay on one line as
// `± key: value`, only complex values nest. Mirrors the readable
// experience the test report asks for in I11-G.
func TestRenderYAMLPatchAsDiff_ScalarsStayOnOneLine(t *testing.T) {
	t.Parallel()

	patch := map[string]any{
		"replicas":    3,
		"healthCheck": "/health",
	}
	server := map[string]any{
		"replicas":    1,
		"healthCheck": "",
	}

	got := renderYAMLPatchAsDiff(patch, server)

	wantLines := []string{
		"+ healthCheck: /health",
		"- replicas: 1",
		"+ replicas: 3",
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("expected line containing %q in:\n%s", want, got)
		}
	}
}
