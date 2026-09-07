package dynacmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
	"github.com/spf13/cobra"
)

// Objective 92 / story 212. The objective asks the command line to CONFIRM
// that every new vLLM router and LMCache manifest field is reachable, rather
// than assume it. These tests quantify over `testdata/vllm/new-fields.json`,
// which is derived by `scripts/vllm_field_diff.py` from two checked-in
// manifest snapshots. See testdata/vllm/PROVENANCE.md for how the snapshots
// were produced and which conductor commit each came from.
//
// Nothing here reaches the network beyond the test's own httptest server, and
// no account, service or customer is named: the ids below are placeholders.
//
// A future field addition is a REGENERATION of the artefact, not a rewrite of
// these tests.

const vllmArtefactDir = "../../testdata/vllm"

// vllmNewFields mirrors the parts of new-fields.json these tests quantify
// over. Fields the generator writes but no criterion reads are left out
// deliberately, so an added attribute cannot silently change a test.
type vllmNewFields struct {
	Post struct {
		ManifestVersion string `json:"manifestVersion"`
		ConductorCommit string `json:"conductorCommit"`
	} `json:"post"`
	Counts struct {
		DistinctNewFieldNames int `json:"distinctNewFieldNames"`
	} `json:"counts"`
	Commands []struct {
		Command        string `json:"command"`
		NewInputFields []struct {
			Name  string `json:"name"`
			Type  string `json:"type"`
			Class string `json:"class"`
		} `json:"newInputFields"`
		NewOutputFields []struct {
			Name string `json:"name"`
		} `json:"newOutputFields"`
	} `json:"commands"`
	WriteOnlyFields []string `json:"writeOnlyFields"`
}

// vllmSnapshot mirrors a manifest snapshot. `commands` decodes straight into
// manifest.Command, so the tests build from the same struct the loader hands
// the builder at runtime.
type vllmSnapshot struct {
	ManifestVersion       string             `json:"manifestVersion"`
	ConductorCommit       string             `json:"conductorCommit"`
	Commands              []manifest.Command `json:"commands"`
	AdvancedConfigSchemas map[string]struct {
		Type   string `json:"type"`
		Fields []struct {
			Key        string `json:"key"`
			InputType  string `json:"inputType"`
			ValueShape string `json:"valueShape"`
			Delivery   string `json:"delivery"`
		} `json:"fields"`
	} `json:"advancedConfigSchemas"`
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func loadVLLMNewFields(t *testing.T) vllmNewFields {
	t.Helper()
	var set vllmNewFields
	readJSON(t, filepath.Join(vllmArtefactDir, "new-fields.json"), &set)
	if len(set.Commands) == 0 {
		t.Fatal("new-fields.json lists no commands: regenerate it with scripts/vllm_field_diff.py")
	}
	return set
}

// loadVLLMPostSnapshot globs rather than naming the version, so regenerating
// against a later build is dropping one file in and deleting the old one,
// not editing three test files. Exactly one post snapshot may exist: two
// would make "the post build" ambiguous, which is the mistake the manifest
// version string already invites (45.5.0 names two different manifests).
func loadVLLMPostSnapshot(t *testing.T) vllmSnapshot {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(vllmArtefactDir, "manifest-post-*.json"))
	if err != nil {
		t.Fatalf("glob post snapshot: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one manifest-post-*.json in %s, found %d: %v",
			vllmArtefactDir, len(matches), matches)
	}
	var snap vllmSnapshot
	readJSON(t, matches[0], &snap)
	return snap
}

// findLeafCommand walks the built tree the way a user's argv does, so the
// command under test is the one the builder actually produced rather than one
// the test assembled.
func findLeafCommand(t *testing.T, top []*cobra.Command, manifestPath string) *cobra.Command {
	t.Helper()
	var want []string
	for _, part := range strings.Split(displayPathFor(manifestPath), "/") {
		if !placeholderRegex.MatchString(part) {
			want = append(want, part)
		}
	}
	var cur *cobra.Command
	for _, c := range top {
		if c.Name() == want[0] {
			cur = c
			break
		}
	}
	if cur == nil {
		t.Fatalf("%s: no top-level command %q in the built tree", manifestPath, want[0])
	}
	for _, part := range want[1:] {
		var next *cobra.Command
		for _, child := range cur.Commands() {
			if child.Name() == part {
				next = child
				break
			}
		}
		if next == nil {
			t.Fatalf("%s: %q has no child %q", manifestPath, cur.Name(), part)
		}
		cur = next
	}
	return cur
}

func buildFromSnapshot(t *testing.T, snap vllmSnapshot) []*cobra.Command {
	t.Helper()
	m := &manifest.Manifest{Version: snap.ManifestVersion, Commands: snap.Commands}
	return NewBuilder(m, NewExecutor("http://127.0.0.1:1")).BuildCommands()
}

// pflagTypeForManifestType is the flag type the builder's registration switch
// produces for each manifest type. Object and array both register StringArray:
// object because a repeatable `key=value` must survive a comma in the value,
// array because pflag's StringSlice splits on commas inside a JSON element.
var pflagTypeForManifestType = map[string]string{
	"string":  "string",
	"integer": "int",
	"boolean": "bool",
	"object":  "stringArray",
	"array":   "stringArray",
}

// --- Criterion 2: every new input field is a flag, spelled predictably -----

func TestEveryNewVLLMInputFieldIsReachableAsAFlag(t *testing.T) {
	set := loadVLLMNewFields(t)
	top := buildFromSnapshot(t, loadVLLMPostSnapshot(t))

	checked := 0
	for _, cmd := range set.Commands {
		if len(cmd.NewInputFields) == 0 {
			continue
		}
		leaf := findLeafCommand(t, top, cmd.Command)
		for _, field := range cmd.NewInputFields {
			checked++
			wantName := flagNameFor(field.Name)

			flag := leaf.Flags().Lookup(wantName)
			if flag == nil {
				t.Errorf("%s: field %q (%s) has no --%s flag; an operator cannot set it",
					cmd.Command, field.Name, field.Type, wantName)
				continue
			}
			if flag.Name != wantName {
				t.Errorf("%s: field %q resolved to --%s, want --%s",
					cmd.Command, field.Name, flag.Name, wantName)
			}

			// The criterion's reason for pinning the spelling is that an
			// operator copies the API field name out of the docs. Looking the
			// RAW manifest name up exercises the flag normalizer, which is
			// what makes `--routerImage` and `--extra_env` land on the
			// canonical kebab flag instead of "unknown flag".
			if raw := leaf.Flags().Lookup(field.Name); raw == nil || raw.Name != wantName {
				t.Errorf("%s: the manifest spelling --%s does not resolve to --%s; "+
					"an operator copying the API field name gets an unknown-flag error",
					cmd.Command, field.Name, wantName)
			}

			wantType, known := pflagTypeForManifestType[field.Type]
			if !known {
				t.Errorf("%s: field %q declares manifest type %q, which the builder's "+
					"registration switch has no arm for, so no flag type is defined for it",
					cmd.Command, field.Name, field.Type)
				continue
			}
			if got := flag.Value.Type(); got != wantType {
				t.Errorf("%s: --%s registered as pflag %q, want %q for manifest type %q",
					cmd.Command, wantName, got, wantType, field.Type)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no new input fields were checked; the artefact quantifies over nothing")
	}
	t.Logf("checked %d new input field(s) across %d command(s) from conductor %s (manifest %s)",
		checked, len(set.Commands), set.Post.ConductorCommit, set.Post.ManifestVersion)
}

// --- Criterion 3: object-typed fields, and the map that is not one ---------

// bodyRecordingStub answers 200 and records the request body it received, so
// a test can assert what actually went on the wire rather than what the CLI
// intended to send.
func bodyRecordingStub(t *testing.T, got *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		*got = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jobId":"job1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEveryNewObjectTypedVLLMFieldTakesWholeJSON is criterion 3's first half.
// It quantifies over the object-typed new input fields in the artefact. On the
// snapshot in this repo that set is EMPTY, which is a finding rather than a
// vacuum: conductor declares every advanced-config field as `string`. The
// companion test below is the one that carries the content.
func TestEveryNewObjectTypedVLLMFieldTakesWholeJSON(t *testing.T) {
	set := loadVLLMNewFields(t)
	snap := loadVLLMPostSnapshot(t)
	byCommand := map[string]manifest.Command{}
	for _, c := range snap.Commands {
		byCommand[c.Command] = c
	}

	objectFields := 0
	for _, cmd := range set.Commands {
		for _, field := range cmd.NewInputFields {
			if field.Type != "object" {
				continue
			}
			objectFields++
			cmdDef, ok := byCommand[cmd.Command]
			if !ok {
				t.Fatalf("%s is in new-fields.json but not in the post snapshot", cmd.Command)
			}

			var sent []byte
			srv := bodyRecordingStub(t, &sent)
			warnEnv(t, localhostURL(srv.URL))
			leaf := findLeafCommand(t, buildFromSnapshot(t, snap), cmd.Command)
			const whole = `{"KEY_ONE":"1","KEY_TWO":"two"}`
			if err := leaf.Flags().Set(flagNameFor(field.Name), whole); err != nil {
				t.Fatalf("set --%s: %v", flagNameFor(field.Name), err)
			}
			exec := NewExecutor(localhostURL(srv.URL))
			if err := exec.Execute(leaf, []string{"svc1"}, cmdDef); err != nil {
				t.Fatalf("%s: Execute: %v", cmd.Command, err)
			}

			var body map[string]any
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("%s: request body is not JSON: %v (%q)", cmd.Command, err, sent)
			}
			var want map[string]any
			_ = json.Unmarshal([]byte(whole), &want)
			got, _ := json.Marshal(body[field.Name])
			wantJSON, _ := json.Marshal(want)
			if string(got) != string(wantJSON) {
				t.Errorf("%s: --%s sent %s, want the whole JSON object %s verbatim",
					cmd.Command, flagNameFor(field.Name), got, wantJSON)
			}
		}
	}
	t.Logf("object-typed new input fields in the artefact: %d", objectFields)
}

// vllmDeclaredStringButJSONShaped names every catalog key whose CATALOG says
// the value is a JSON object while the MANIFEST declares it `string`.
//
// This is not a CLI carve-out: no production code reads it. It is the record
// that keeps NOTES-manifest.md honest. The conductor mapper
// (src/util/cliManifest/advancedConfigFields.ts) flattens every advanced-config
// field to `string` with no branch on the catalog's own type, so a map-shaped
// key gets ONE opaque string flag instead of the repeatable `--flag key=value`
// form. Recorded as a conductor change due in NOTES-manifest.md section 5;
// the value stays REACHABLE meanwhile, as a JSON string.
//
// The test below fails if a NEW json-shaped key appears that is not listed
// here, and fails if an entry here goes stale because conductor fixed the
// declaration. Both directions matter: the first stops a gap shipping
// unrecorded, the second stops the record outliving the defect.
var vllmDeclaredStringButJSONShaped = map[string][]string{
	"vllm-router":    {"extra_env"},
	"lmcache-server": {"l2_adapters", "runtime_plugin_config"},
}

// vllmCatalogCommand maps a catalog service type to the manifest command that
// carries its keys as input fields.
var vllmCatalogCommand = map[string]string{
	"vllm-router":    "services/vllm/{id}/set-router-config",
	"lmcache-server": "services/vllm/{id}/set-lmcache-server-config",
}

func TestJSONShapedCatalogKeysAreEitherDeclaredObjectOrRecorded(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	byCommand := map[string]manifest.Command{}
	for _, c := range snap.Commands {
		byCommand[c.Command] = c
	}

	notes, err := os.ReadFile("../../NOTES-manifest.md")
	if err != nil {
		t.Fatalf("read NOTES-manifest.md: %v", err)
	}

	seen := map[string]map[string]bool{}
	jsonShaped := 0
	for catalogType, schema := range snap.AdvancedConfigSchemas {
		cmdPath, ok := vllmCatalogCommand[catalogType]
		if !ok {
			t.Fatalf("catalog %q is in the snapshot but no command is mapped for it", catalogType)
		}
		cmdDef, ok := byCommand[cmdPath]
		if !ok {
			t.Fatalf("%s is missing from the post snapshot", cmdPath)
		}
		declared := map[string]string{}
		if cmdDef.Input != nil {
			for _, f := range cmdDef.Input.Fields {
				declared[f.Name] = f.Type
			}
		}
		seen[catalogType] = map[string]bool{}

		for _, field := range schema.Fields {
			if field.ValueShape != "json" {
				continue
			}
			jsonShaped++
			manifestType, present := declared[field.Key]
			if !present {
				t.Errorf("%s: catalog key %q is json-shaped but the manifest declares no "+
					"input field for it, so it is unreachable from the command line",
					catalogType, field.Key)
				continue
			}
			if manifestType == "object" {
				continue // Declared correctly; the whole-JSON test above covers it.
			}
			seen[catalogType][field.Key] = true

			if !containsString(vllmDeclaredStringButJSONShaped[catalogType], field.Key) {
				t.Errorf("%s: catalog key %q is json-shaped but the manifest declares %q, "+
					"and it is not recorded as a known gap. Either conductor declares it "+
					"`object`, or add it to vllmDeclaredStringButJSONShaped AND to "+
					"NOTES-manifest.md so it is not unreachable-and-unrecorded",
					catalogType, field.Key, manifestType)
				continue
			}
			if !strings.Contains(string(notes), field.Key) {
				t.Errorf("%s: catalog key %q is listed as a known gap here but NOTES-manifest.md "+
					"does not name it; the conductor change due would be lost",
					catalogType, field.Key)
			}
		}
	}

	// The record must not outlive the defect.
	for catalogType, keys := range vllmDeclaredStringButJSONShaped {
		for _, key := range keys {
			if !seen[catalogType][key] {
				t.Errorf("%s: %q is recorded as a json-shaped-but-string gap, but the snapshot "+
					"no longer shows one. If conductor fixed the declaration, delete this entry "+
					"and the matching NOTES-manifest.md text", catalogType, key)
			}
		}
	}

	if jsonShaped == 0 {
		t.Fatal("no json-shaped catalog key found in either schema; the cross-check is vacuous")
	}
	t.Logf("checked %d json-shaped catalog key(s)", jsonShaped)
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- The artefact itself --------------------------------------------------

// TestVLLMArtefactIsInternallyConsistent guards the artefact rather than the
// CLI: a hand-edit that drifts from the snapshots it claims to describe would
// otherwise weaken every test above without failing any of them.
func TestVLLMArtefactIsInternallyConsistent(t *testing.T) {
	set := loadVLLMNewFields(t)
	snap := loadVLLMPostSnapshot(t)

	if set.Post.ConductorCommit != snap.ConductorCommit {
		t.Errorf("new-fields.json says it came from conductor %q, the post snapshot says %q",
			set.Post.ConductorCommit, snap.ConductorCommit)
	}

	byCommand := map[string]manifest.Command{}
	for _, c := range snap.Commands {
		byCommand[c.Command] = c
	}
	names := map[string]bool{}
	for _, cmd := range set.Commands {
		cmdDef, ok := byCommand[cmd.Command]
		if !ok {
			t.Fatalf("%s is in new-fields.json but not in the post snapshot", cmd.Command)
		}
		declared := map[string]string{}
		if cmdDef.Input != nil {
			for _, f := range cmdDef.Input.Fields {
				declared[f.Name] = f.Type
			}
		}
		for _, f := range cmd.NewInputFields {
			names[f.Name] = true
			if got, ok := declared[f.Name]; !ok {
				t.Errorf("%s: new-fields.json lists input field %q that the post snapshot "+
					"does not declare", cmd.Command, f.Name)
			} else if got != f.Type {
				t.Errorf("%s: new-fields.json types %q as %q, the post snapshot says %q",
					cmd.Command, f.Name, f.Type, got)
			}
		}
		outputs := map[string]bool{}
		if cmdDef.Output != nil {
			for _, o := range cmdDef.Output.Fields {
				outputs[o.Name] = true
			}
		}
		for _, f := range cmd.NewOutputFields {
			names[f.Name] = true
			if !outputs[f.Name] {
				t.Errorf("%s: new-fields.json lists output field %q that the post snapshot "+
					"does not declare", cmd.Command, f.Name)
			}
		}
	}
	if len(names) != set.Counts.DistinctNewFieldNames {
		t.Errorf("new-fields.json counts %d distinct new field names, its own commands list %d",
			set.Counts.DistinctNewFieldNames, len(names))
	}

	// Criterion 6's set, recomputed here so the summary's list cannot drift
	// from the artefact. The CLI synthesizes no read path for these.
	writeOnly := writeOnlyVLLMFields(byCommand)
	recorded := append([]string(nil), set.WriteOnlyFields...)
	sort.Strings(recorded)
	if strings.Join(writeOnly, ",") != strings.Join(recorded, ",") {
		t.Errorf("write-only fields recomputed from the post snapshot are %v, "+
			"new-fields.json records %v", writeOnly, recorded)
	}
}

// writeOnlyVLLMFields is add/update input minus show output: a field an
// operator can write but never read back, so the IaC pull cannot project it
// and `services diff` reports drift the operator cannot clear.
func writeOnlyVLLMFields(byCommand map[string]manifest.Command) []string {
	written := map[string]bool{}
	for _, path := range []string{"services/vllm/add", "services/vllm/{id}/update"} {
		cmdDef := byCommand[path]
		if cmdDef.Input == nil {
			continue
		}
		for _, f := range cmdDef.Input.Fields {
			if !f.Positional {
				written[f.Name] = true
			}
		}
	}
	readable := map[string]bool{}
	if show := byCommand["services/vllm/{id}/show"]; show.Output != nil {
		for _, o := range show.Output.Fields {
			readable[o.Name] = true
		}
	}
	var out []string
	for name := range written {
		if !readable[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
