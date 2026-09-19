package services

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Objective 102 / story 263. Conductor manifest 48.26.0 reversed the
// per-type clearable-key rule: omitting a key now PRESERVES the stored
// value and removal is the field's explicit empty value. These tests pin
// the consumer half: `runos services sync` sends that empty value
// for a key the operator deleted from the yaml, and that it does so for
// the marked fields ONLY.
//
// The guard case matters as much as the removal. A rule of the shape
// "send the zero value for every allowed field the file omits" would put
// `replicas: 0` on the wire for the same update command and stop a lane,
// so every test below that proves a removal has a sibling proving that
// nothing else moved.
//
// Every id, name and tag below is a placeholder; nothing names a real
// account, cluster, service or customer.

const (
	clearablePinField    = "nodeAffinityTags"
	clearableStringField = "placementNote"
)

// clearableManifest builds the postgresql surface the way conductor
// declares it at manifest 48.26.0: the pin is an ARRAY field marked
// clearable on the UPDATE command and unmarked on ADD (removal is an
// update-time act), and the show command returns it so a pulled file can
// carry it.
//
// `placementNote` is a STRING field carrying the same marker. No real
// type declares a clearable string today (every marked field in 48.26.0
// is the array-typed pin), but the marker's contract names `""` as the
// empty value of a string field, so the CLI's handling of it is pinned
// here rather than left to be discovered by the first type that adopts
// one.
//
// `replicas` is the guard: an integer field on the same update command,
// unmarked, whose zero value is destructive.
func clearableManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	return &manifest.Manifest{
		Version: "48.26.0-test",
		Commands: []manifest.Command{
			{
				Command:  "services/postgresql/{id}/show",
				Method:   "GET",
				Endpoint: "/:aid/:cid/services/postgresql/:id",
				Output: &manifest.Output{Fields: namesAsFields(
					"id", "name", "replicas", clearablePinField, clearableStringField,
				)},
			},
			{
				Command:  "services/postgresql/add",
				Method:   "POST",
				Endpoint: "/:aid/:cid/services/postgresql",
				Input: &manifest.Input{Fields: []manifest.Field{
					{Name: "name", Type: "string"},
					{Name: "replicas", Type: "integer"},
					{Name: clearablePinField, Type: "array"},
					{Name: clearableStringField, Type: "string"},
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
					{Name: clearablePinField, Type: "array", Clearable: true},
					{Name: clearableStringField, Type: "string", Clearable: true},
				}},
			},
		},
	}
}

// clearableCommands returns the three commands ComputeSyncPlan takes.
func clearableCommands(t *testing.T, m *manifest.Manifest) (add, update, show *manifest.Command) {
	t.Helper()
	var err error
	if add, err = AddCommand(m, "postgresql"); err != nil {
		t.Fatalf("add command: %v", err)
	}
	if update, err = UpdateCommand(m, "postgresql"); err != nil {
		t.Fatalf("update command: %v", err)
	}
	if show, err = ShowCommand(m, "postgresql"); err != nil {
		t.Fatalf("show command: %v", err)
	}
	return add, update, show
}

func clearableService(fields map[string]any) *ServiceYAML {
	return &ServiceYAML{
		Type:   "postgresql",
		ID:     "abc12",
		CID:    syncTestClusterID,
		AID:    syncTestAccountID,
		Fields: fields,
	}
}

// applyAndRecord applies the plan against a recording stub and returns
// the single request body that reached the wire.
func applyAndRecord(t *testing.T, plan *SyncPlan, addCmd, updateCmd *manifest.Command) map[string]any {
	t.Helper()
	var seen [][]byte
	srv := recordingStub(t, `{"jobId":"job-1"}`, &seen)
	syncEnv(t, srv.URL)
	if _, err := ApplySyncPlan(dynacmd.NewExecutor(srv.URL), plan, addCmd, updateCmd); err != nil {
		t.Fatalf("ApplySyncPlan: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected exactly one request, saw %d", len(seen))
	}
	var body map[string]any
	if err := json.Unmarshal(seen[0], &body); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, seen[0])
	}
	return body
}

// CRITERION 2. A yaml that omits a clearable field the server still
// holds sends that field's explicit empty value, in the field's declared
// shape.
func TestSyncSendsTheExplicitEmptyValueForADeletedClearableField(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	server := clearableService(map[string]any{
		"name":               "lane-one",
		"replicas":           float64(2),
		clearablePinField:    []any{"tag-a", "tag-b"},
		clearableStringField: "rack-7",
	})
	// The operator deleted both clearable lines and lowered the count.
	local := clearableService(map[string]any{
		"name":     "lane-one",
		"replicas": 1,
	})

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)

	gotPin, ok := plan.PatchBody[clearablePinField]
	if !ok {
		t.Fatalf("plan omits %s entirely, so the deleted pin never reaches the wire; body=%#v",
			clearablePinField, plan.PatchBody)
	}
	if arr, isArr := gotPin.([]any); !isArr || len(arr) != 0 {
		t.Errorf("%s = %#v, want an empty array", clearablePinField, gotPin)
	}
	if got := plan.PatchBody[clearableStringField]; got != "" {
		t.Errorf("%s = %#v, want an empty string", clearableStringField, got)
	}
	wantRemovals := []string{clearablePinField, clearableStringField}
	assertStrings(t, "Removals", plan.Removals, wantRemovals)
	if len(plan.Refused) != 0 {
		t.Errorf("a removal was refused: %v", plan.Refused)
	}

	// Assert what actually went on the wire, not only what the plan held.
	body := applyAndRecord(t, plan, addCmd, updateCmd)
	raw, err := json.Marshal(body[clearablePinField])
	if err != nil {
		t.Fatalf("marshal recorded pin: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("wire body carries %s=%s, want []", clearablePinField, raw)
	}
	if got := body[clearableStringField]; got != "" {
		t.Errorf("wire body carries %s=%#v, want an empty string", clearableStringField, got)
	}
	if got := body["replicas"]; got != float64(1) {
		t.Errorf("wire body carries replicas=%#v, want 1", got)
	}
}

// D6. A file whose ONLY edit is a deleted clearable key must still
// produce a PATCH. Before this story computeDriftPatch walked the local
// keys only, so it returned nil here and the plan reported a difference
// in its rendered diff that the apply could not resolve.
func TestSyncAPureRemovalStillProducesAPatchBody(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	server := clearableService(map[string]any{
		"name":            "lane-one",
		"replicas":        float64(2),
		clearablePinField: []any{"tag-a"},
	})
	local := clearableService(map[string]any{
		"name":     "lane-one",
		"replicas": float64(2),
	})

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	if !plan.HasChanges() {
		t.Fatal("a file whose only edit is a deleted pin produced no changes, so sync would do nothing")
	}
	if len(plan.PatchBody) != 1 {
		t.Errorf("PatchBody = %#v, want the removal alone", plan.PatchBody)
	}
	assertStrings(t, "Removals", plan.Removals, []string{clearablePinField})

	body := applyAndRecord(t, plan, addCmd, updateCmd)
	// The executor adds `id` for the path placeholder; nothing else may
	// ride along with a pure removal.
	delete(body, "id")
	if len(body) != 1 {
		t.Errorf("wire body = %#v, want the removal alone", body)
	}
}

// CRITERION 3. A yaml that carries the same value as the server sends no
// entry for that field, so no false removal is possible from an
// unchanged file.
func TestSyncSendsNoEntryForAnUnchangedClearableField(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	fields := func() map[string]any {
		return map[string]any{
			"name":            "lane-one",
			"replicas":        float64(2),
			clearablePinField: []any{"tag-a", "tag-b"},
		}
	}
	server := clearableService(fields())
	local := clearableService(fields())
	local.Fields["name"] = "lane-two" // one unrelated edit, so there IS a patch

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	if _, present := plan.PatchBody[clearablePinField]; present {
		t.Errorf("an unchanged pin reached the wire as %#v; an unchanged file must remove nothing",
			plan.PatchBody[clearablePinField])
	}
	if len(plan.Removals) != 0 {
		t.Errorf("Removals = %v, want none", plan.Removals)
	}
}

// CRITERION 4, THE GUARD. A field the manifest does NOT mark, omitted
// from the yaml, sends nothing. The field named here is `replicas`, whose zero
// value would stop a lane.
func TestSyncNeverClearsAFieldTheManifestDoesNotMark(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	server := clearableService(map[string]any{
		"name":            "lane-one",
		"replicas":        float64(3),
		clearablePinField: []any{"tag-a"},
	})
	// The operator deleted the replica count AND the pin. Only the pin
	// is marked, so only the pin is cleared.
	local := clearableService(map[string]any{"name": "lane-two"})

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	if _, present := plan.PatchBody["replicas"]; present {
		t.Errorf("replicas reached the plan body as %#v; an unmarked field must never be cleared",
			plan.PatchBody["replicas"])
	}
	assertStrings(t, "Removals", plan.Removals, []string{clearablePinField})

	body := applyAndRecord(t, plan, addCmd, updateCmd)
	if _, present := body["replicas"]; present {
		t.Errorf("wire body carries replicas=%#v; the lane would scale", body["replicas"])
	}
	// Belt and braces: no zero replica count anywhere in the body, under
	// any spelling the encoder might produce.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal recorded body: %v", err)
	}
	for _, forbidden := range []string{`"replicas":0`, `"replicas": 0`, `"replicas":"0"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("wire body contains %s: %s", forbidden, raw)
		}
	}
}

// CRITERION 5, the JSON half. The plan's `removals` key carries the same
// names the text plan prints. The text half lives in
// cmd/services_sync_removals_test.go, where the renderer is.
func TestSyncPlanJSONCarriesTheRemovals(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	server := clearableService(map[string]any{
		"name":            "lane-one",
		clearablePinField: []any{"tag-a"},
	})
	local := clearableService(map[string]any{"name": "lane-one"})

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	var decoded struct {
		Removals []string `json:"removals"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	assertStrings(t, "removals", decoded.Removals, []string{clearablePinField})

	// A plan with nothing to remove carries no `removals` key at all,
	// so a CI gate keying on its presence is not tripped by every sync.
	quiet := ComputeSyncPlan(clearableService(map[string]any{
		"name":            "lane-two",
		clearablePinField: []any{"tag-a"},
	}), server, addCmd, updateCmd, showCmd)
	quietRaw, err := json.Marshal(quiet)
	if err != nil {
		t.Fatalf("marshal quiet plan: %v", err)
	}
	if strings.Contains(string(quietRaw), "removals") {
		t.Errorf("a plan with no removal still carries a removals key: %s", quietRaw)
	}
}

// CRITERION 7. A pull, one unrelated edit, then a sync invents no
// removal: pull writes no clearable field the show response did not
// carry, and the sync that follows sends no empty value for a field the
// server does not hold.
func TestPullThenUnrelatedEditInventsNoRemoval(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	// The server holds no pin, so its show response carries none.
	show := map[string]any{
		"id":       "abc12",
		"name":     "lane-one",
		"replicas": float64(2),
	}
	pulled := BuildPulledService(show, "postgresql", syncTestClusterID, syncTestAccountID, "abc12", addCmd, updateCmd)
	if _, present := pulled.Fields[clearablePinField]; present {
		t.Errorf("pull wrote %s into the yaml from a show response that did not carry it", clearablePinField)
	}

	server := BuildPulledService(show, "postgresql", syncTestClusterID, syncTestAccountID, "abc12", addCmd, updateCmd)
	edited := clearableService(map[string]any{})
	for k, v := range pulled.Fields {
		edited.Fields[k] = v
	}
	edited.Fields["replicas"] = float64(3) // the one unrelated edit

	plan := ComputeSyncPlan(edited, server, addCmd, updateCmd, showCmd)
	if _, present := plan.PatchBody[clearablePinField]; present {
		t.Errorf("sync invented a removal for %s, which the server does not hold: %#v",
			clearablePinField, plan.PatchBody)
	}
	if len(plan.Removals) != 0 {
		t.Errorf("Removals = %v, want none", plan.Removals)
	}

	// The same holds when the server explicitly holds the EMPTY value:
	// deleting a key that is already empty asks for nothing.
	emptyServer := clearableService(map[string]any{
		"name":            "lane-one",
		clearablePinField: []any{},
	})
	emptyLocal := clearableService(map[string]any{"name": "lane-two"})
	emptyPlan := ComputeSyncPlan(emptyLocal, emptyServer, addCmd, updateCmd, showCmd)
	if _, present := emptyPlan.PatchBody[clearablePinField]; present {
		t.Errorf("sync sent a removal for an already-empty %s: %#v", clearablePinField, emptyPlan.PatchBody)
	}
}

// D5. A type whose show command does not return the pin cannot have a
// removal invented for it, because the server projection the plan diffs
// against is the show response. This is what keeps a write-only field
// safe by construction rather than by a carve-out; conductor's
// `services/langfuse` is that case at 48.26.0, and it is deliberately
// not marked clearable either (RunOS item 462).
func TestNoRemovalForAFieldTheServerProjectionCannotHold(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, showCmd := clearableCommands(t, m)

	// Show returned no pin, so BuildPulledService cannot project one,
	// even though the running service may well carry one.
	server := BuildPulledService(map[string]any{
		"id":       "abc12",
		"name":     "lane-one",
		"replicas": float64(2),
	}, "postgresql", syncTestClusterID, syncTestAccountID, "abc12", addCmd, updateCmd)

	local := clearableService(map[string]any{"name": "lane-two", "replicas": float64(2)})
	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	if _, present := plan.PatchBody[clearablePinField]; present {
		t.Errorf("a field the show response never carried was cleared: %#v", plan.PatchBody)
	}
	if len(plan.Removals) != 0 {
		t.Errorf("Removals = %v, want none", plan.Removals)
	}
}

// D3. A marked field whose declared type has no empty value the CLI
// knows how to send produces NO wire entry and one refused line. This is
// the second half of the criterion-4 guard: it is what keeps a
// mis-marked `replicas` off the wire rather than relying on conductor
// never mis-marking one.
func TestAMarkedFieldOfAnUnhandledTypeIsRefusedNotGuessed(t *testing.T) {
	m := clearableManifest(t)
	updateCmd, err := UpdateCommand(m, "postgresql")
	if err != nil {
		t.Fatalf("update command: %v", err)
	}
	// Mis-mark the guard field the way a conductor slip would.
	for i := range updateCmd.Input.Fields {
		if updateCmd.Input.Fields[i].Name == "replicas" {
			updateCmd.Input.Fields[i].Clearable = true
		}
	}
	addCmd, _, showCmd := clearableCommands(t, m)

	server := clearableService(map[string]any{"name": "lane-one", "replicas": float64(3)})
	local := clearableService(map[string]any{"name": "lane-two"})

	plan := ComputeSyncPlan(local, server, addCmd, updateCmd, showCmd)
	if _, present := plan.PatchBody["replicas"]; present {
		t.Fatalf("a mis-marked integer field put %#v on the wire; a guessed empty shape stops a lane",
			plan.PatchBody["replicas"])
	}
	if len(plan.Removals) != 0 {
		t.Errorf("Removals = %v, want none: nothing was removed", plan.Removals)
	}
	var found string
	for _, r := range plan.Refused {
		if strings.HasPrefix(r, "replicas:") {
			found = r
		}
	}
	if found == "" {
		t.Fatalf("no refused line names replicas; the operator is told nothing. Refused = %v", plan.Refused)
	}
	if !strings.Contains(found, `"integer"`) {
		t.Errorf("the refused line does not name the declared type: %q", found)
	}
}

// ClearableFields reads the marker and nothing else: the ADD command is
// never marked, a positional path parameter is not a body key, and an
// unmarked field of a marked type stays unmarked.
func TestClearableFieldsReadsTheMarkerOnly(t *testing.T) {
	m := clearableManifest(t)
	addCmd, updateCmd, _ := clearableCommands(t, m)

	got := ClearableFields(updateCmd)
	if len(got) != 2 {
		t.Fatalf("ClearableFields(update) = %v, want exactly the two marked fields", keysOf(got))
	}
	for _, name := range []string{clearablePinField, clearableStringField} {
		if _, ok := got[name]; !ok {
			t.Errorf("ClearableFields(update) is missing %s", name)
		}
	}
	if _, ok := got["replicas"]; ok {
		t.Error("ClearableFields(update) marked replicas, which carries no marker")
	}
	if n := len(ClearableFields(addCmd)); n != 0 {
		t.Errorf("ClearableFields(add) = %v, want none: removal is an update-time act", keysOf(ClearableFields(addCmd)))
	}
	if n := len(ClearableFields(nil)); n != 0 {
		t.Errorf("ClearableFields(nil) returned %d entries", n)
	}
}

// The two empty shapes the marker's contract names, and the refusal for
// everything else.
func TestEmptyValueForCoversTheContractsTwoShapes(t *testing.T) {
	if v, ok := emptyValueFor("array"); !ok {
		t.Error("array has no empty value")
	} else if arr, isArr := v.([]any); !isArr || len(arr) != 0 {
		t.Errorf("empty array value = %#v", v)
	}
	if v, ok := emptyValueFor("string"); !ok || v != "" {
		t.Errorf("empty string value = %#v, ok=%v", v, ok)
	}
	for _, declared := range []string{"integer", "boolean", "object", ""} {
		if _, ok := emptyValueFor(declared); ok {
			t.Errorf("%q was given an empty value; only array and string have one", declared)
		}
	}
}

func keysOf(m map[string]manifest.Field) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}
