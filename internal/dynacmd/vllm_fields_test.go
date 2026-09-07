package dynacmd

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
			// unsetBy names how an operator CLEARS the value. Every field on
			// this build says "empty-string", which is why a typed flag would
			// remove the unset path (NOTES-manifest.md section 5, blocker 2).
			UnsetBy string `json:"unsetBy"`

			Step *float64 `json:"step"`
			Min  *float64 `json:"min"`
			Max  *float64 `json:"max"`
			// suggestedDefault is a STRING in the catalog ("0.3", "24") and is
			// null when the field has no default.
			SuggestedDefault *string `json:"suggestedDefault"`
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

// --- The change-due record ------------------------------------------------

// notesSection5 returns the body of NOTES-manifest.md section 5, the record
// criterion 7 requires for a gap this story does not fix in the CLI.
func notesSection5(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../NOTES-manifest.md")
	if err != nil {
		t.Fatalf("read NOTES-manifest.md: %v", err)
	}
	notes := string(raw)
	start := strings.Index(notes, "## 5. ")
	if start < 0 {
		t.Fatal("NOTES-manifest.md has no section 5; the class-B gap would be unrecorded")
	}
	end := strings.Index(notes[start:], "\n## 6. ")
	if end < 0 {
		return notes[start:]
	}
	return notes[start : start+end]
}

// TestNotesSection5MatchesTheSnapshot pins the change-due record to the
// artefact it claims to describe.
//
// Review cycle 2 found section 5 measured against the PRE-change build while
// the story bounds the POST one, so its census understated the gap and put a
// key that already exists in the future tense. Nothing could catch that,
// because the only check on the record was that it contained a key's name
// somewhere. A record a conductor implementer cannot trust is worse than no
// record, so the numbers and the key lists are now derived from the snapshot
// and compared, and regenerating the artefact against a later build fails here
// rather than dating the note silently.
func TestNotesSection5MatchesTheSnapshot(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	section := notesSection5(t)

	if !strings.Contains(section, snap.ConductorCommit) {
		t.Errorf("section 5 does not name conductor %s, the build its census is measured on; "+
			"the manifest version is not an identifier (45.5.0 names two manifests)",
			snap.ConductorCommit)
	}

	for catalogType, schema := range snap.AdvancedConfigSchemas {
		var number, toggle, jsonShaped int
		var toggleKeys, jsonKeys []string
		for _, f := range schema.Fields {
			switch f.InputType {
			case "number":
				number++
			case "toggle":
				toggle++
				toggleKeys = append(toggleKeys, f.Key)
			}
			if f.ValueShape == "json" {
				jsonShaped++
				jsonKeys = append(jsonKeys, f.Key)
			}
		}

		// The row is `| `<catalog>` | fields | number | toggle | json | fields |`.
		wantRow := fmt.Sprintf("| `%s` | %d | %d | %d | %d | %d |",
			catalogType, len(schema.Fields), number, toggle, jsonShaped, len(schema.Fields))
		if !strings.Contains(section, wantRow) {
			t.Errorf("section 5's census has no row matching the snapshot for %q.\n"+
				"  want row: %s\n"+
				"  re-measure section 5 against the checked-in post snapshot",
				catalogType, wantRow)
		}

		// Cost 2 names the toggle keys and cost 3 names the json-shaped ones.
		// A key the record does not name is a key the conductor implementer
		// does not know to fix.
		for _, key := range toggleKeys {
			if !strings.Contains(section, key) {
				t.Errorf("%s: toggle key %q is not named in section 5, so the switch cost the "+
					"section itself lists is under-recorded", catalogType, key)
			}
		}
		for _, key := range jsonKeys {
			if !strings.Contains(section, key) {
				t.Errorf("%s: json-shaped key %q is not named in section 5, so the map-flag cost "+
					"is under-recorded", catalogType, key)
			}
		}
	}
}

// prescribedMapping returns the "> left -> right" lines of section 5, which
// are the mapping a conductor implementer applies. Checking the whole section
// is not enough: a property name that appears in the census table would mask a
// prescription that branches on a property no field carries.
func prescribedMapping(t *testing.T, section string) []string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "> ") && strings.Contains(trimmed, "->") {
			lines = append(lines, strings.TrimPrefix(trimmed, "> "))
		}
	}
	if len(lines) == 0 {
		t.Fatal("section 5 prescribes no mapping; there is nothing for conductor to apply")
	}
	return lines
}

// TestNotesSection5PrescribesPropertiesTheCatalogCarries guards the thing that
// makes the record unusable rather than merely stale: a prescribed mapping
// that branches on a property no published catalog field has. Review cycle 2
// found `type: 'integer'` prescribed where the catalog publishes
// `inputType: 'number'`, so the arm covering most of the gap matched nothing.
//
// The assertion is on the LEFT side of each mapping arm, checked against the
// key union of the snapshot, because that is the side a conductor implementer
// has to find in their own code.
func TestNotesSection5PrescribesPropertiesTheCatalogCarries(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	section := notesSection5(t)

	carried := map[string]bool{}
	for _, schema := range snap.AdvancedConfigSchemas {
		for _, f := range schema.Fields {
			if f.InputType != "" {
				carried["inputType"] = true
			}
			if f.ValueShape != "" {
				carried["valueShape"] = true
			}
			if f.Delivery != "" {
				carried["delivery"] = true
			}
			carried["key"] = true
		}
	}
	if !carried["inputType"] || !carried["valueShape"] {
		t.Fatal("the snapshot carries neither inputType nor valueShape; this test's premise is wrong")
	}

	produced := map[string]bool{}
	for _, arm := range prescribedMapping(t, section) {
		parts := strings.SplitN(arm, "->", 2)
		left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		produced[right] = true

		// "everything else" is the default arm and names no property.
		if !strings.Contains(left, ":") {
			continue
		}
		property := strings.TrimSpace(strings.SplitN(strings.Trim(left, "`"), ":", 2)[0])
		if !carried[property] {
			t.Errorf("section 5 prescribes branching on %q, which NO field in the snapshot "+
				"carries. A conductor implementer applying this arm matches nothing. The "+
				"published properties are inputType, valueShape and delivery.", property)
		}
	}

	// Each arm the mapping must produce. `boolean` is here because section 5
	// lists the switch cost, and a mapping without a boolean arm does not fix it.
	for _, want := range []string{"`type: 'object'`", "`type: 'integer'`", "`type: 'boolean'`"} {
		if !produced[want] && !strings.Contains(strings.Join(prescribedMapping(t, section), "\n"), want) {
			t.Errorf("section 5's prescribed mapping has no arm producing %s, so a cost it "+
				"lists would survive the change it prescribes", want)
		}
	}
}

// catalogField is one advanced-config field as the snapshot publishes it.
type catalogField = struct {
	Key        string `json:"key"`
	InputType  string `json:"inputType"`
	ValueShape string `json:"valueShape"`
	Delivery   string `json:"delivery"`
	UnsetBy    string `json:"unsetBy"`

	Step             *float64 `json:"step"`
	Min              *float64 `json:"min"`
	Max              *float64 `json:"max"`
	SuggestedDefault *string  `json:"suggestedDefault"`
}

// fractionalCatalogField reports whether a numeric catalog field carries
// fractional values. `inputType: number` is only a UI hint; `step` is where the
// catalog says whether the value is a whole number.
func fractionalCatalogField(f catalogField) bool {
	if f.Step != nil && *f.Step != math.Trunc(*f.Step) {
		return true
	}
	for _, v := range []*float64{f.Min, f.Max} {
		if v != nil && *v != math.Trunc(*v) {
			return true
		}
	}
	return f.SuggestedDefault != nil && strings.Contains(*f.SuggestedDefault, ".")
}

// prescribedManifestType is NOTES-manifest.md section 5's mapping in
// EXECUTABLE form. The section is prose a conductor implementer applies by
// hand; this is the same rule as code, so the test below can check what the
// prescription PRODUCES rather than only how it is spelled.
//
// Keep the two in step. TestNotesSection5PrescribesPropertiesTheCatalogCarries
// checks the section still spells these arms; this function is what decides
// whether they are right.
func prescribedManifestType(f catalogField) string {
	switch {
	case f.ValueShape == "json":
		return "object"
	case f.InputType == "toggle":
		return "boolean"
	case f.InputType == "number" && !fractionalCatalogField(f):
		return "integer"
	default:
		// Fractional numbers land here deliberately: there is no float
		// manifest type and no float arm in the registration switch, so
		// `integer` would refuse the field's own default.
		return "string"
	}
}

// buildOneFieldCommand registers a single manifest field through the REAL
// builder, so the flag under test is the one an operator would get.
func buildOneFieldCommand(t *testing.T, fieldName, manifestType string) *cobra.Command {
	t.Helper()
	cmdDef := manifest.Command{
		Command:  "services/vllm/{id}/set-router-config",
		Endpoint: "/:aid/:cid/services/vllm/:id/set-router-config",
		Method:   http.MethodPost,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
			{Name: fieldName, Type: manifestType},
		}},
	}
	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	return findLeafCommand(t, NewBuilder(m, NewExecutor("http://127.0.0.1:1")).BuildCommands(), cmdDef.Command)
}

// TestNotesSection5PrescriptionAcceptsEveryFieldsOwnDefault is the check that
// the earlier section-5 tests could not make.
//
// Review cycle 3 found the prescribed `inputType: 'number' -> integer` arm was
// UNCONDITIONAL, so applying it verbatim would have retyped seven fractional
// fields to a ParseInt flag that refuses their own suggested defaults —
// turning reachable fields unreachable, which is the exact harm this story
// exists to prevent. The cycle-2 tests missed it because they only validated
// the LEFT side of each arm: an arm that fires and produces the wrong type
// passed.
//
// So this one applies the prescription to every catalog field in the snapshot
// and asserts the resulting flag ACCEPTS that field's own suggestedDefault. A
// prescription that cannot carry the catalog's own default value is wrong,
// whatever it is spelled.
func TestNotesSection5PrescriptionAcceptsEveryFieldsOwnDefault(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)

	checked := 0
	for catalogType, schema := range snap.AdvancedConfigSchemas {
		for _, f := range schema.Fields {
			if f.SuggestedDefault == nil || *f.SuggestedDefault == "" {
				continue // No default to carry.
			}
			manifestType := prescribedManifestType(f)
			if _, known := pflagTypeForManifestType[manifestType]; !known {
				t.Errorf("%s/%s: the prescription produces manifest type %q, which the "+
					"builder's registration switch has no arm for", catalogType, f.Key, manifestType)
				continue
			}
			checked++

			leaf := buildOneFieldCommand(t, f.Key, manifestType)
			flagName := flagNameFor(f.Key)

			// A field can declare a fractional step and still have a WHOLE
			// suggested default (l1_size_gb: step 0.01, default "20"), so the
			// default alone would not reveal a wrong type. Probe the step
			// value too, which is by definition a legal increment.
			if f.Step != nil && *f.Step != math.Trunc(*f.Step) {
				probe := strconv.FormatFloat(*f.Step, 'f', -1, 64)
				if err := leaf.Flags().Set(flagName, probe); err != nil {
					t.Errorf("%s/%s: NOTES section 5 prescribes manifest type %q, but the flag "+
						"it builds REFUSES the field's own step %q: %v\n"+
						"  A fractional field typed %q cannot take a fractional value.",
						catalogType, f.Key, manifestType, probe, err, manifestType)
				}
				leaf = buildOneFieldCommand(t, f.Key, manifestType)
			}

			if err := leaf.Flags().Set(flagName, *f.SuggestedDefault); err != nil {
				t.Errorf("%s/%s: NOTES section 5 prescribes manifest type %q, but the flag it "+
					"builds REFUSES the field's own suggested default %q: %v\n"+
					"  (inputType=%q valueShape=%q step=%v)\n"+
					"  A prescription that makes a reachable field unreachable is wrong; "+
					"re-read the conditional number arm in section 5.",
					catalogType, f.Key, manifestType, *f.SuggestedDefault, err,
					f.InputType, f.ValueShape, f.Step)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no catalog field carried a suggested default; the prescription was never applied")
	}
	t.Logf("applied section 5's prescription to %d catalog field(s) with a default", checked)
}

// TestNotesSection5NamesEveryFractionalKey keeps the human half of the record
// in step with the executable half: a fractional key the section does not name
// is a carve-out the conductor implementer has to infer.
func TestNotesSection5NamesEveryFractionalKey(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	section := notesSection5(t)

	fractional := 0
	for catalogType, schema := range snap.AdvancedConfigSchemas {
		for _, f := range schema.Fields {
			if f.InputType != "number" || !fractionalCatalogField(f) {
				continue
			}
			fractional++
			if !strings.Contains(section, f.Key) {
				t.Errorf("%s: fractional key %q is not named in section 5, so a reader cannot "+
					"see which fields the conditional number arm covers", catalogType, f.Key)
			}
		}
	}
	if fractional == 0 {
		t.Fatal("no fractional number field found in the snapshot; the carve-out is vacuous")
	}
	t.Logf("section 5 names all %d fractional number key(s)", fractional)
}

// buildRecordingCommand registers ONE manifest field of the given type through
// the real builder and points it at a body-recording stub, so what the test
// reads is the body an operator's invocation would actually send.
func buildRecordingCommand(t *testing.T, fieldName, manifestType string, sent *[]byte) (*cobra.Command, manifest.Command, *Executor) {
	t.Helper()
	srv := bodyRecordingStub(t, sent)
	warnEnv(t, localhostURL(srv.URL))
	cmdDef := manifest.Command{
		Command:  "services/vllm/{id}/set-router-config",
		Endpoint: "/:aid/:cid/services/vllm/:id/set-router-config",
		Method:   http.MethodPost,
		Input: &manifest.Input{Fields: []manifest.Field{
			{Name: "id", Type: "string", Positional: true, Required: true},
			{Name: fieldName, Type: manifestType},
		}},
	}
	m := &manifest.Manifest{Commands: []manifest.Command{cmdDef}}
	exec := NewExecutor(localhostURL(srv.URL))
	leaf := findLeafCommand(t, NewBuilder(m, exec).BuildCommands(), cmdDef.Command)
	return leaf, cmdDef, exec
}

// sendOneField declares ONE manifest field of the given type, sets it to
// value, runs it through the real executor, and returns what the request body
// carried for that field. Reading the body is the point: the earlier section-5
// tests all stopped at the flag.
func sendOneField(t *testing.T, fieldName, manifestType, value string) any {
	t.Helper()
	var sent []byte
	leaf, cmdDef, exec := buildRecordingCommand(t, fieldName, manifestType, &sent)
	if err := leaf.Flags().Set(flagNameFor(fieldName), value); err != nil {
		t.Fatalf("declared %q: set --%s %q: %v", manifestType, flagNameFor(fieldName), value, err)
	}
	if err := exec.Execute(leaf, []string{"svc1"}, cmdDef); err != nil {
		t.Fatalf("declared %q: Execute: %v", manifestType, err)
	}
	var body map[string]any
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("request body is not JSON: %v (%q)", err, sent)
	}
	return body[fieldName]
}

// TestRetypingWouldSendANonStringBody measures NOTES-manifest.md section 5's
// FIRST blocker, at the WIRE rather than at the flag.
//
// The set-*-config family is string-valued by contract: foreman #40 was this
// exact failure on the -f path, where a YAML `queue_size: 128` reached the
// wire as a number and conductor refused it with "expected a string but got
// number" (see coerceBodyFileValue in executor.go and its test). The flag path
// has the same contract, so a retyped field breaks it the same way.
//
// Every earlier section-5 test stopped at flag registration — cycle 2 checked
// the left side of each arm, cycle 3 checked that the arm produced a flag the
// field's own default could pass through. None of them asked what the field
// then PUT IN THE BODY. So this one sends every catalog field's own
// suggestedDefault through the real executor, twice: as declared today, and
// under the type the mapping would give it.
func TestRetypingWouldSendANonStringBody(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	section := notesSection5(t)

	total, wouldStopBeingAString, measured := 0, 0, 0
	for catalogType, schema := range snap.AdvancedConfigSchemas {
		for _, f := range schema.Fields {
			total++
			prescribed := prescribedManifestType(f)
			if prescribed != "string" {
				wouldStopBeingAString++
			}
			if f.SuggestedDefault == nil || *f.SuggestedDefault == "" {
				continue // Nothing of the field's own to send.
			}
			measured++

			// As DECLARED today the body carries a JSON string, which is what
			// the Record<string,string> validator requires.
			if got := sendOneField(t, f.Key, "string", *f.SuggestedDefault); !isJSONString(got) {
				t.Errorf("%s/%s: declared `string` today, the body carries %#v, not a JSON "+
					"string; blocker 1's premise is wrong and must be re-measured",
					catalogType, f.Key, got)
			}
			if prescribed == "string" {
				continue
			}

			// Retyped, the same value stops being a string on the wire. That
			// is the regression, so assert it rather than assume it.
			if got := sendOneField(t, f.Key, prescribed, *f.SuggestedDefault); isJSONString(got) {
				t.Errorf("%s/%s: retyped %q the body still carries the string %#v, so section "+
					"5's blocker 1 OVERSTATES the cost; re-measure it",
					catalogType, f.Key, prescribed, got)
			}
		}
	}

	// The three json-shaped keys carry no suggestedDefault, so the loop above
	// never exercises the `object` arm. extra_env is the one that matters:
	// section 5 says it works TODAY as a JSON string through conductor's
	// parseRouterExtraEnv, and the mapping would stop sending that string.
	const wholeJSON = `{"HF_HUB_OFFLINE":"1"}`
	if got := sendOneField(t, "extra_env", "string", wholeJSON); !isJSONString(got) {
		t.Errorf("extra_env declared `string` sent %#v, not the JSON string section 5 says "+
			"parseRouterExtraEnv reads", got)
	}
	if got := sendOneField(t, "extra_env", "object", wholeJSON); isJSONString(got) {
		t.Errorf("extra_env declared `object` still sent a string (%#v); section 5's "+
			"parseRouterExtraEnv precondition would be unnecessary", got)
	}

	if measured == 0 {
		t.Fatal("no catalog field was sent; blocker 1 is unmeasured")
	}
	// The record states the count, so derive it here rather than trust prose.
	wantCount := fmt.Sprintf("%d of the %d", wouldStopBeingAString, total)
	if !strings.Contains(section, wantCount) {
		t.Errorf("section 5's blocker 1 does not say %q; measured %d of %d fields that stop "+
			"sending a JSON string when retyped", wantCount, wouldStopBeingAString, total)
	}
	// The precedent and the parser precondition are the two things a conductor
	// implementer acts on. Neither may be dropped by a later edit.
	// Each phrase is chosen to be unique to the statement it stands for.
	// "parseRouterExtraEnv" alone is not enough: the section names that parser
	// elsewhere just to say extra_env works today, so matching the bare name
	// would pass even with the precondition deleted.
	for _, want := range []string{
		"foreman #40",
		"Record<string,string>",
		"must accept an object as well as a JSON string",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("section 5 no longer states %q, so a reader could apply the mapping "+
				"without knowing what it breaks", want)
		}
	}
	t.Logf("sent %d catalog default(s) through the executor; %d of %d keys stop sending a "+
		"JSON string when retyped", measured, wouldStopBeingAString, total)
}

// isJSONString reports whether a decoded JSON body value is a string. A number
// decodes to float64 and a boolean to bool, which is the whole distinction
// blocker 1 turns on.
func isJSONString(v any) bool {
	_, ok := v.(string)
	return ok
}

// TestTheBodyFileStillCarriesTheUnsetAfterRetyping is the correction the
// fourth review cycle earned.
//
// An earlier draft of blocker 2 said retyping removes the ONLY way to unset a
// field. That was too strong: coerceBodyFileValue returns a value it cannot
// convert UNCHANGED (foreman #40's deliberate escape hatch), so an empty
// string in a -f body file still reaches the wire whatever the field is
// declared. The loss is on the FLAG surface, and the record now says that.
//
// If this ever stops being true the correction becomes wrong, so it is
// measured rather than asserted.
func TestTheBodyFileStillCarriesTheUnsetAfterRetyping(t *testing.T) {
	for _, manifestType := range []string{"string", "integer", "boolean"} {
		var sent []byte
		leaf, cmdDef, exec := buildRecordingCommand(t, "max_concurrent_requests", manifestType, &sent)

		path := filepath.Join(t.TempDir(), "body.yaml")
		if err := os.WriteFile(path, []byte("max_concurrent_requests: \"\"\n"), 0o600); err != nil {
			t.Fatalf("write body file: %v", err)
		}
		if err := leaf.Flags().Set("file", path); err != nil {
			t.Fatalf("set --file: %v", err)
		}
		if err := exec.Execute(leaf, []string{"svc1"}, cmdDef); err != nil {
			t.Fatalf("declared %q: Execute: %v", manifestType, err)
		}

		var body map[string]any
		if err := json.Unmarshal(sent, &body); err != nil {
			t.Fatalf("declared %q: body is not JSON: %v (%q)", manifestType, err, sent)
		}
		if got := body["max_concurrent_requests"]; got != "" {
			t.Errorf("declared %q, a -f body file carrying the empty-string unset sent %#v, "+
				"want \"\". Section 5 blocker 2 calls the -f path the surviving unset route; "+
				"if that is no longer true, retyping loses the unset ENTIRELY and the record "+
				"understates the cost.", manifestType, got)
		}
	}

	if section := notesSection5(t); !strings.Contains(section, "FLAG-SURFACE LOSS") {
		t.Error("section 5 no longer distinguishes the flag-surface loss from total " +
			"unreachability, which is the distinction this test exists to keep honest")
	}
}

// TestRetypingWouldRemoveTheEmptyStringUnsetPath measures section 5's SECOND
// blocker, the one the operator decision named as decisive.
//
// Every advanced-config field declares `unsetBy: 'empty-string'`: an operator
// clears a value by sending "". The CLI has no concept of `unsetBy` — nothing
// in this repo reads it — so that empty string is just the value, and only a
// `string` flag will carry it. Retyping a field `integer` or `boolean` makes
// the flag refuse "" before it can reach the wire, which removes the only
// unset path the catalog has.
//
// The test quantifies over the snapshot rather than over an example, so the
// count in section 5 stays honest as the catalog grows.
func TestRetypingWouldRemoveTheEmptyStringUnsetPath(t *testing.T) {
	snap := loadVLLMPostSnapshot(t)
	section := notesSection5(t)

	total, wouldLose := 0, 0
	for catalogType, schema := range snap.AdvancedConfigSchemas {
		for _, f := range schema.Fields {
			total++
			if f.UnsetBy != "empty-string" {
				t.Errorf("%s/%s declares unsetBy %q, not \"empty-string\"; section 5's "+
					"blocker 2 is derived from that value and must be re-measured",
					catalogType, f.Key, f.UnsetBy)
				continue
			}

			// As declared today the field is `string`, and the unset value goes.
			asIs := buildOneFieldCommand(t, f.Key, "string")
			if err := asIs.Flags().Set(flagNameFor(f.Key), ""); err != nil {
				t.Errorf("%s/%s: the field as DECLARED today refuses the empty string that "+
					"unsetBy depends on: %v", catalogType, f.Key, err)
			}

			retyped := prescribedManifestType(f)
			if retyped != "integer" && retyped != "boolean" {
				continue // string and object both carry "".
			}
			wouldLose++
			leaf := buildOneFieldCommand(t, f.Key, retyped)
			if err := leaf.Flags().Set(flagNameFor(f.Key), ""); err == nil {
				t.Errorf("%s/%s: retyped %q the flag ACCEPTS the empty string, so section 5's "+
					"blocker 2 overstates the cost; re-measure it", catalogType, f.Key, retyped)
			}
		}
	}

	if wouldLose == 0 {
		t.Fatal("no field would lose its unset path; blocker 2 would be vacuous")
	}
	// Section 5 states the count. A record whose numbers drift is the failure
	// mode review cycle 2 found, so derive it here rather than trusting prose.
	wantCount := fmt.Sprintf("%d of the %d", wouldLose, total)
	if !strings.Contains(section, wantCount) {
		t.Errorf("section 5's blocker 2 does not say %q; measured %d of %d fields lose the "+
			"empty-string unset path when retyped", wantCount, wouldLose, total)
	}
	if !strings.Contains(section, "unsetBy") {
		t.Error("section 5 does not name `unsetBy`, so a reader cannot check blocker 2 " +
			"against the catalog themselves")
	}
	t.Logf("%d of %d catalog fields would lose the empty-string unset path if retyped",
		wouldLose, total)
}
