package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// The account maintenance view renders words the SERVER derived. These
// tests assert the story's criteria, never this renderer's current
// output, and they treat the fixture as the contract the server ships.
// Provenance of every fixture: testdata/maintenance/PROVENANCE.md.

const maintenanceFixtureDir = "../../testdata/maintenance"

// maintenanceOutputDef is the manifest entry conductor registers for
// `nodes/maintenance`: one object with one declared field. The entry
// names no node row field, which is why the renderer carries its own
// column order.
func maintenanceOutputDef() *manifest.Output {
	return &manifest.Output{Type: "object", Fields: []manifest.OutputField{{Name: "clusters"}}}
}

func readMaintenanceFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(maintenanceFixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("fixture %s is not valid JSON: %v", name, err)
	}
	return raw
}

func renderMaintenanceFixture(t *testing.T, name string) string {
	t.Helper()
	data := readMaintenanceFixture(t, name)
	return captureStdout(t, func() {
		if err := NewFormatter(false).Format(data, maintenanceOutputDef()); err != nil {
			t.Fatalf("format %s: %v", name, err)
		}
	})
}

// T2. Criterion 3. The server's own word reaches the screen for a node
// RunOS cannot read and for a node whose facts are too old. Neither
// node may render as a gap.
func TestMaintenanceViewRendersServerStatusWord(t *testing.T) {
	out := renderMaintenanceFixture(t, "full.json")

	for _, want := range []string{"Blocked", "Unknown", "Up to date", "Updates waiting", "Both"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered view is missing the server status word %q\n--- output ---\n%s", want, out)
		}
	}

	// The word has to sit on the row of the node it belongs to, not
	// merely somewhere in the output.
	for _, pair := range []struct{ node, status string }{
		{"control-plane-one", "Up to date"},
		{"worker-one", "Both"},
		{"worker-two", "Blocked"},
		{"worker-three", "Unknown"},
		{"single-node", "Updates waiting"},
	} {
		if !lineWithBoth(out, pair.node, pair.status) {
			t.Errorf("no single line carries node %q and its server status %q\n--- output ---\n%s",
				pair.node, pair.status, out)
		}
	}
}

// T3. Criterion 3. Every named column of a node whose facts are absent
// shows the absent marker. A blank cell reads as nothing-wrong, which
// is the defect this view exists to remove.
func TestMaintenanceViewMarksAbsentFactsAndNeverBlanks(t *testing.T) {
	out := renderMaintenanceFixture(t, "full.json")

	// worker-two is offline: the server sent null for every fact.
	row := lineContaining(out, "worker-two")
	if row == "" {
		t.Fatalf("no row for the offline node\n--- output ---\n%s", out)
	}
	if strings.Count(row, absentMarker) < 5 {
		t.Errorf("the offline node's row must mark every absent fact; got %d markers in %q",
			strings.Count(row, absentMarker), row)
	}
	if blankRunPattern.MatchString(row) {
		t.Errorf("the offline node's row carries a blank cell: %q", row)
	}

	// The marker is visibly not a status, so a reader cannot read a
	// gap as a verdict.
	if strings.Contains(absentMarker, "date") || !strings.HasPrefix(absentMarker, "(") {
		t.Errorf("absent marker %q must not resemble a status word", absentMarker)
	}
}

// T1. Criteria 1, 2 and 4. The whole response renders: every cluster
// header, every node row column, and every row detail fact.
func TestMaintenanceViewRendersEveryClusterNodeAndDetail(t *testing.T) {
	out := renderMaintenanceFixture(t, "full.json")

	for _, cluster := range []string{"cluster-one", "ca001", "cluster-two", "cb002"} {
		if !strings.Contains(out, cluster) {
			t.Errorf("rendered view is missing cluster identity %q", cluster)
		}
	}
	for _, node := range []string{"control-plane-one", "worker-one", "worker-two", "worker-three", "single-node"} {
		if !strings.Contains(out, node) {
			t.Errorf("rendered view is missing node %q", node)
		}
	}

	// Criterion 2 columns, by their header text.
	for _, header := range []string{
		"STATUS", "LAST BOOT AGE", "REBOOT PENDING AGE", "SECURITY UPDATES",
		"LAST SUCCESSFUL RUN", "AGENT VERSION", "AGENT STATE", "AUTO UPDATE UNITS",
	} {
		if !strings.Contains(out, header) {
			t.Errorf("node table is missing the column for %q\n--- output ---\n%s", header, out)
		}
	}

	// The cluster header facts.
	for _, want := range []string{"1.12.3", "v1.31.4", "v1.31.6", "live", "none"} {
		if !strings.Contains(out, want) {
			t.Errorf("cluster header is missing %q", want)
		}
	}

	// Criterion 4 row detail facts, from the node that carries all of them.
	for _, want := range []string{
		"libssl3", "openssl", "libc-bin", // waiting packages
		"linux-image-arm64",                // the reboot trigger
		"6.1.0-18-arm64", "6.1.0-21-arm64", // running and newest installed kernel
		"containerd", "1.7.22-1", "runc", "1.1.14-1", // held packages with versions
	} {
		if !strings.Contains(out, want) {
			t.Errorf("row detail is missing %q\n--- output ---\n%s", want, out)
		}
	}

	// The server's sentences reach the reader, including the per-node
	// automatic update unit sentence the objective's criterion 9 needs.
	for _, want := range []string{
		"3 security updates are waiting, and a reboot is pending.",
		"The automatic update timers on this node were enabled outside RunOS.",
		"The node agent is not connected, so RunOS cannot read this node.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("row detail is missing the server sentence %q", want)
		}
	}
}

// T8. Criterion 7. A stale fact shows its own time, so a reader can
// tell a stale count from a current one. The age comes from the
// server's sentence; the time is the server's collected-at value.
func TestMaintenanceViewShowsFactAgeForAStaleFact(t *testing.T) {
	out := renderMaintenanceFixture(t, "full.json")

	if !strings.Contains(out, "2026-11-08T18:02:00Z") {
		t.Errorf("the stale node's collected-at value must print\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "are 4 hours old, so RunOS does not know its current state") {
		t.Errorf("the server's own staleness sentence must print")
	}
	// The stale node still reported a count of 2. The reader must be
	// able to see both the count and the fact's time.
	if !lineWithBoth(out, "worker-three", "Unknown") {
		t.Errorf("the stale node must carry its server status word on its row")
	}
}

// T4. Criterion 5. A status word this build has never seen renders
// verbatim. The test fails if a future edit adds a word list, a
// mapping or a default.
func TestMaintenanceViewRendersAnUnrecognisedServerWordVerbatim(t *testing.T) {
	out := renderMaintenanceFixture(t, "invented-status.json")

	for _, want := range []string{
		"Quarantined by a later release",
		"A status word this CLI build has never seen.",
		"A label this build has never seen",
		"a-unit-state-this-build-has-never-seen",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("an unrecognised server value must render verbatim; missing %q\n--- output ---\n%s", want, out)
		}
	}

	// R5: a server fact the renderer does not recognise is never
	// dropped.
	for _, want := range []string{"quarantineReleasesAt", "2026-11-09T00:00:00Z", "quarantineEvidence"} {
		if !strings.Contains(out, want) {
			t.Errorf("an unrecognised server field must still reach the reader; missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// T7. Criterion 1 and 3 at the edges. An account with no cluster and a
// cluster with no node each say so in words.
func TestMaintenanceViewRendersEmptySets(t *testing.T) {
	empty := renderMaintenanceFixture(t, "empty-account.json")
	if !strings.Contains(empty, "no clusters") {
		t.Errorf("an account with no cluster must say so\n--- output ---\n%s", empty)
	}

	noNodes := renderMaintenanceFixture(t, "cluster-without-nodes.json")
	if !strings.Contains(noNodes, "cluster-three") {
		t.Errorf("a cluster with no node must still print its header\n--- output ---\n%s", noNodes)
	}
	if !strings.Contains(noNodes, "no nodes") {
		t.Errorf("a cluster with no node must say so\n--- output ---\n%s", noNodes)
	}
}

// T6. Every other response keeps the output it has today. The empty
// case matters: `clusters/list` answers `{"clusters":[...]}` as well,
// so an empty list is the shape this hook could wrongly capture.
func TestMaintenanceViewLeavesOtherResponsesAlone(t *testing.T) {
	for _, name := range []string{"clusters-list.json", "clusters-list-empty.json"} {
		data := readMaintenanceFixture(t, name)
		def := &manifest.Output{Type: "array", Fields: []manifest.OutputField{{Name: "cid"}, {Name: "name"}}}
		out := captureStdout(t, func() {
			if err := NewFormatter(false).Format(data, def); err != nil {
				t.Fatalf("format %s: %v", name, err)
			}
		})
		if strings.Contains(out, absentMarker) {
			t.Errorf("%s must not reach the maintenance renderer; output: %q", name, out)
		}
		if strings.Contains(out, "no clusters") || strings.Contains(out, "no nodes") {
			t.Errorf("%s must not reach the maintenance renderer; output: %q", name, out)
		}
	}
}

// T9. The `--json` path carries the server response unchanged. The MCP
// read tools run the CLI with `--json`, so this is also the proof that
// the MCP surface holds no second derivation.
func TestMaintenanceViewJSONPathCarriesEveryFact(t *testing.T) {
	data := readMaintenanceFixture(t, "full.json")
	out := captureStdout(t, func() {
		if err := NewFormatter(true).Format(data, maintenanceOutputDef()); err != nil {
			t.Fatalf("format json: %v", err)
		}
	})

	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the --json output is not valid JSON: %v\n%s", err, out)
	}
	wantBytes, _ := json.Marshal(want)
	gotBytes, _ := json.Marshal(got)
	if string(wantBytes) != string(gotBytes) {
		t.Errorf("the --json output must carry the server response unchanged\nwant %s\ngot  %s", wantBytes, gotBytes)
	}
}

// T5. Criterion 5, mechanized. The renderer source carries no contract
// status word as a literal and no version comparison, so a later edit
// cannot quietly put a second derivation back into this client.
func TestMaintenanceViewSourceHoldsNoSecondDerivation(t *testing.T) {
	source, err := os.ReadFile("maintenance_view.go")
	if err != nil {
		t.Fatalf("read renderer source: %v", err)
	}
	text := string(source)

	// Contract B's six values and their six display labels. The
	// renderer prints what the server sent, so none of these may
	// appear in its source at all.
	for _, word := range []string{
		"up_to_date", "updates_waiting", "reboot_pending", "both", "unknown", "blocked",
		"Up to date", "Updates waiting", "Reboot pending", "Both", "Unknown", "Blocked",
	} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(text) {
			t.Errorf("maintenance_view.go carries the contract status word %q; the server owns the words", word)
		}
	}

	// The automatic update unit values are a server vocabulary too.
	for _, word := range []string{"enabled_outside_runos", "masked", "runos_managed"} {
		if strings.Contains(text, word) {
			t.Errorf("maintenance_view.go carries the server's unit-state value %q", word)
		}
	}

	// No version comparison, and no clock arithmetic on a server time.
	for _, banned := range []string{"semver", "CompareVersion", "compareVersion", "time.Since", "time.Now", "Sub("} {
		if strings.Contains(text, banned) {
			t.Errorf("maintenance_view.go must hold no version comparison and no clock arithmetic; found %q", banned)
		}
	}
}

// blankRunPattern finds a cell position that rendered as nothing: three
// or more spaces between two column separators at the end of a cell.
// A rendered row that matches has a gap where a fact belongs.
var blankRunPattern = regexp.MustCompile(`\s{3,}$`)

func lineContaining(out, needle string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func lineWithBoth(out, a, b string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}
