package services

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/runos-official/cli/internal/dynacmd"
	"github.com/runos-official/cli/internal/manifest"
)

// Objective 92 / story 212, criterion 5: for a vLLM service, the pull command
// writes every new field the show command returns, an immediately following
// diff reports no drift, and the sync command sends every new field back
// through add or update. Exercised against a stubbed conductor.
//
// The set of "new fields" is testdata/vllm/new-fields.json, derived by
// scripts/vllm_field_diff.py from two checked-in manifest snapshots; see
// testdata/vllm/PROVENANCE.md. This test builds its manifest from the POST
// snapshot, so it drives the real ShowCommand / AddCommand / UpdateCommand
// lookups against the shape conductor actually emits, not a hand-written one.
//
// Every id and value below is a placeholder; nothing names a real account,
// service, registry or customer.

const vllmArtefactDir = "../../testdata/vllm"

type vllmNewFields struct {
	Post struct {
		ManifestVersion string `json:"manifestVersion"`
		ConductorCommit string `json:"conductorCommit"`
	} `json:"post"`
	Commands []struct {
		Command        string `json:"command"`
		NewInputFields []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"newInputFields"`
		NewOutputFields []struct {
			Name string `json:"name"`
		} `json:"newOutputFields"`
	} `json:"commands"`
	WriteOnlyFields []string `json:"writeOnlyFields"`
}

type vllmSnapshot struct {
	ManifestVersion string             `json:"manifestVersion"`
	Commands        []manifest.Command `json:"commands"`
}

func loadVLLMArtefact(t *testing.T) (vllmNewFields, *manifest.Manifest) {
	t.Helper()
	var set vllmNewFields
	raw, err := os.ReadFile(filepath.Join(vllmArtefactDir, "new-fields.json"))
	if err != nil {
		t.Fatalf("read new-fields.json: %v", err)
	}
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("parse new-fields.json: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(vllmArtefactDir, "manifest-post-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one manifest-post-*.json, found %v (err %v)", matches, err)
	}
	var snap vllmSnapshot
	raw, err = os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse %s: %v", matches[0], err)
	}
	return set, &manifest.Manifest{Version: snap.ManifestVersion, Commands: snap.Commands}
}

// newFieldsFor returns the artefact's new input field names for one command.
func newInputFieldsFor(set vllmNewFields, command string) []string {
	var out []string
	for _, c := range set.Commands {
		if c.Command != command {
			continue
		}
		for _, f := range c.NewInputFields {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

func newOutputFieldsFor(set vllmNewFields, command string) []string {
	var out []string
	for _, c := range set.Commands {
		if c.Command != command {
			continue
		}
		for _, f := range c.NewOutputFields {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// vllmShowResponse is a stub show body carrying every new output field plus
// the handful of existing ones the projection reads. The image values are
// placeholders in a registry host that does not resolve.
func vllmShowResponse(set vllmNewFields) map[string]any {
	body := map[string]any{
		"id":                         "svc12",
		"name":                       "lane-one",
		"type":                       "vllm",
		"model":                      "org/model-1b",
		"servedModelName":            "local-alias",
		"version":                    "0.25.1",
		"resourceRequirementClassId": "vllm.c0.small",
		"gpuCount":                   float64(1),
		"lmcacheMode":                "remote",
	}
	for i, name := range newOutputFieldsFor(set, "services/vllm/{id}/show") {
		if name == "lmcacheServerGeneration" {
			body[name] = "current"
			continue
		}
		body[name] = fmt.Sprintf("registry.invalid/placeholder-%d:v1", i)
	}
	return body
}

// recordingStub answers every request with respBody and records each request
// body it received, so the test can assert what actually went on the wire.
func recordingStub(t *testing.T, respBody string, seen *[][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*seen = append(*seen, raw)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVLLMNewFieldsRoundTripThroughPullDiffAndSync(t *testing.T) {
	set, m := loadVLLMArtefact(t)

	showCmd, err := ShowCommand(m, "vllm")
	if err != nil {
		t.Fatalf("show command: %v", err)
	}
	addCmd, err := AddCommand(m, "vllm")
	if err != nil {
		t.Fatalf("add command: %v", err)
	}
	updateCmd, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatalf("update command: %v", err)
	}

	show := vllmShowResponse(set)
	showJSON, err := json.Marshal(show)
	if err != nil {
		t.Fatalf("marshal show body: %v", err)
	}

	// --- PULL: every new field the show command returns lands in the yaml.
	var seen [][]byte
	srv := recordingStub(t, string(showJSON), &seen)
	syncEnv(t, srv.URL)
	exec := dynacmd.NewExecutor(srv.URL)

	pulled, err := Pull(exec, m, "vllm", syncTestClusterID, syncTestAccountID, "svc12")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	settable := AddInputFieldNames(addCmd)
	for k := range UpdateInputFieldNames(updateCmd) {
		settable[k] = true
	}
	checkedPull := 0
	for _, name := range newOutputFieldsFor(set, "services/vllm/{id}/show") {
		if !settable[name] {
			// Returned but not settable: pull deliberately writes only what
			// an operator can change, so this is correct, not a gap.
			continue
		}
		checkedPull++
		if _, ok := pulled.Fields[name]; !ok {
			t.Errorf("pull dropped %q, which show returns and add/update accept; "+
				"a services diff would report drift the operator cannot clear", name)
		}
	}
	if checkedPull == 0 {
		t.Fatal("no new show-output field was both returned and settable; nothing was proven")
	}

	// --- DIFF: immediately after a pull there is no drift.
	server := BuildPulledService(show, "vllm", syncTestClusterID, syncTestAccountID, "svc12", addCmd, updateCmd)
	plan := ComputeSyncPlan(pulled, server, addCmd, updateCmd, showCmd)
	if plan.HasChanges() {
		t.Errorf("a diff taken immediately after a pull reports changes: create=%v patch=%v",
			plan.CreateBody, plan.PatchBody)
	}
	if plan.Diff != "" {
		t.Errorf("a diff taken immediately after a pull is not empty:\n%s", plan.Diff)
	}
	if len(plan.Refused) != 0 {
		t.Errorf("a pull produced a yaml the update endpoint refuses: %v", plan.Refused)
	}

	// --- SYNC, update path: every new update-input field goes back on the wire.
	edited := &ServiceYAML{
		Type: pulled.Type, ID: pulled.ID, CID: pulled.CID, AID: pulled.AID,
		Fields: map[string]any{},
	}
	for k, v := range pulled.Fields {
		edited.Fields[k] = v
	}
	updateNew := newInputFieldsFor(set, "services/vllm/{id}/update")
	if len(updateNew) == 0 {
		t.Fatal("the artefact lists no new update input fields; the sync half proves nothing")
	}
	for _, name := range updateNew {
		edited.Fields[name] = "registry.invalid/edited:v2"
	}
	edited.Fields["lmcacheServerGeneration"] = "legacy"

	patchPlan := ComputeSyncPlan(edited, server, addCmd, updateCmd, showCmd)
	for _, name := range updateNew {
		if _, ok := patchPlan.PatchBody[name]; !ok {
			t.Errorf("sync plan drops edited field %q, so the change never reaches conductor", name)
		}
	}

	seen = nil
	patchSrv := recordingStub(t, `{"jobId":"job-1"}`, &seen)
	syncEnv(t, patchSrv.URL)
	if _, err := ApplySyncPlan(dynacmd.NewExecutor(patchSrv.URL), patchPlan, addCmd, updateCmd); err != nil {
		t.Fatalf("ApplySyncPlan (update): %v", err)
	}
	assertWireBodyCarries(t, "update", seen, updateNew)

	// --- SYNC, create path: every new add-input field goes back on the wire.
	fresh := &ServiceYAML{
		Type: "vllm", CID: syncTestClusterID, AID: syncTestAccountID,
		Fields: map[string]any{},
	}
	for k, v := range pulled.Fields {
		fresh.Fields[k] = v
	}
	addNew := newInputFieldsFor(set, "services/vllm/add")
	if len(addNew) == 0 {
		t.Fatal("the artefact lists no new add input fields; the create half proves nothing")
	}
	for _, name := range addNew {
		if _, ok := fresh.Fields[name]; !ok {
			fresh.Fields[name] = "registry.invalid/created:v1"
		}
	}

	createPlan := ComputeSyncPlan(fresh, nil, addCmd, updateCmd, showCmd)
	for _, name := range addNew {
		if _, ok := createPlan.CreateBody[name]; !ok {
			t.Errorf("create plan drops %q, so a fresh yaml cannot pin it", name)
		}
	}
	if len(createPlan.Refused) != 0 {
		t.Errorf("create plan refuses fields a pulled yaml legitimately carries: %v", createPlan.Refused)
	}

	seen = nil
	createSrv := recordingStub(t, `{"id":"svc34","jobId":"job-2"}`, &seen)
	syncEnv(t, createSrv.URL)
	if _, err := ApplySyncPlan(dynacmd.NewExecutor(createSrv.URL), createPlan, addCmd, updateCmd); err != nil {
		t.Fatalf("ApplySyncPlan (create): %v", err)
	}
	assertWireBodyCarries(t, "add", seen, addNew)

	t.Logf("round-tripped %d new show-output field(s), %d new update input field(s) and "+
		"%d new add input field(s) from conductor %s (manifest %s)",
		checkedPull, len(updateNew), len(addNew), set.Post.ConductorCommit, set.Post.ManifestVersion)
}

func assertWireBodyCarries(t *testing.T, label string, seen [][]byte, want []string) {
	t.Helper()
	if len(seen) != 1 {
		t.Fatalf("%s: expected exactly one request, saw %d", label, len(seen))
	}
	var body map[string]any
	if err := json.Unmarshal(seen[0], &body); err != nil {
		t.Fatalf("%s: request body is not JSON: %v (%q)", label, err, seen[0])
	}
	for _, name := range want {
		if _, ok := body[name]; !ok {
			t.Errorf("%s: the wire body omits %q; the plan carried it but the request did not",
				label, name)
		}
	}
}

// TestVLLMWriteOnlyFieldsAreRecordedNotSynthesized is criterion 6. A field an
// operator can WRITE but never READ BACK cannot be round-tripped: pull's
// source is the show response, so a field show omits never reaches the yaml,
// and `services diff` reports drift the operator cannot clear.
//
// The CLI must NOT synthesize a read path or a local cache to compensate;
// that is conductor's fix, the same one story 206 just made for `image`. This
// test therefore asserts the recorded set MATCHES the manifest rather than
// asserting the set is empty, so the record cannot drift from reality in
// either direction.
func TestVLLMWriteOnlyFieldsAreRecordedNotSynthesized(t *testing.T) {
	set, m := loadVLLMArtefact(t)
	showCmd, err := ShowCommand(m, "vllm")
	if err != nil {
		t.Fatalf("show command: %v", err)
	}
	addCmd, err := AddCommand(m, "vllm")
	if err != nil {
		t.Fatalf("add command: %v", err)
	}
	updateCmd, err := UpdateCommand(m, "vllm")
	if err != nil {
		t.Fatalf("update command: %v", err)
	}

	readable := map[string]bool{}
	for _, name := range ShowOutputFields(showCmd) {
		readable[name] = true
	}
	written := AddInputFieldNames(addCmd)
	for k := range UpdateInputFieldNames(updateCmd) {
		written[k] = true
	}

	var writeOnly []string
	for name := range written {
		if !readable[name] {
			writeOnly = append(writeOnly, name)
		}
	}
	sort.Strings(writeOnly)

	recorded := append([]string(nil), set.WriteOnlyFields...)
	sort.Strings(recorded)
	if fmt.Sprint(writeOnly) != fmt.Sprint(recorded) {
		t.Errorf("write-only fields computed from the manifest are %v, new-fields.json records %v; "+
			"regenerate the artefact rather than editing either list", writeOnly, recorded)
	}

	// `image` was write-only before objective 92 and is the instance story 206
	// closed. If it reappears here, the read path regressed.
	for _, name := range writeOnly {
		if name == "image" {
			t.Error("`image` is write-only again: story 206 added it to the show output, so a " +
				"pinned engine image would stop round-tripping through pull/diff/sync")
		}
	}
	t.Logf("write-only vLLM fields on conductor %s: %v", set.Post.ConductorCommit, writeOnly)
}
