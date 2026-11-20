package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

const agentsFixtureDir = "../../testdata/agents"

func agentsOutputDef() *manifest.Output {
	return &manifest.Output{
		Type: "object",
		Fields: []manifest.OutputField{
			{Name: "targetVersions.nodeAgent"},
			{Name: "targetVersions.clusterAgent"},
			{Name: "clusters"},
		},
	}
}

func readAgentsFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(agentsFixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("fixture %s is not valid JSON: %v", name, err)
	}
	return raw
}

func renderAgentsFixture(t *testing.T, name string, jsonOutput bool) string {
	t.Helper()
	data := readAgentsFixture(t, name)
	return captureStdout(t, func() {
		if err := NewFormatter(jsonOutput).Format(data, agentsOutputDef()); err != nil {
			t.Fatalf("format %s: %v", name, err)
		}
	})
}

func TestAgentsListRendersOfflineAgentFromServerFields(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	row := lineContaining(out, "node-offline")
	if row == "" {
		t.Fatalf("the output has no node-offline row\n--- output ---\n%s", out)
	}
	for _, want := range []string{
		"control plane + worker", "no_heartbeat", "Offline", absentMarker,
		"false", "The node agent must reconnect before an update can start.",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the offline row is missing %q: %s", want, row)
		}
	}
	for _, unwanted := range []string{"agentState", "updateAvailable", " up_to_date ", " offline "} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the text view exposes the raw server field or value %q\n--- output ---\n%s", unwanted, out)
		}
	}
}

func TestAgentsListRendersVersionlessAgentWithoutInventingVerdict(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	row := lineContaining(out, "node-versionless")
	if row == "" {
		t.Fatalf("the output has no node-versionless row\n--- output ---\n%s", out)
	}
	for _, want := range []string{
		"Version unknown", absentMarker,
		"The node agent must report a version before an update can start.",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the versionless row is missing %q: %s", want, row)
		}
	}
	if strings.Contains(strings.ToLower(row), "up to date") {
		t.Fatalf("the versionless agent reads Up to date: %s", row)
	}
}

func TestAgentsListMarksEveryUnreportedNodeFact(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	row := lineContaining(out, "node-unreported")
	if row == "" {
		t.Fatalf("the output has no node-unreported row\n--- output ---\n%s", out)
	}
	if count := strings.Count(row, absentMarker); count != 6 {
		t.Errorf("the unreported row has %d absent markers, want 6: %s", count, row)
	}
}

func TestAgentsListRendersTimedOutClusterWithoutInventingNodes(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	for _, want := range []string{
		"Cluster cluster-two (cluster-two)",
		"Cluster status: timed_out",
		"The cluster agent did not answer before the timeout.",
		"This cluster reports no nodes.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the timed-out cluster is missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestAgentsListRendersNewServerLabelVerbatim(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	row := lineContaining(out, "node-future")
	if !strings.Contains(row, "A later server label") {
		t.Fatalf("the new server label did not render verbatim: %s", row)
	}
	if strings.Contains(row, "future_state") {
		t.Fatalf("the text view printed the raw state value: %s", row)
	}
}

func TestAgentsListFallsThroughForAnotherShape(t *testing.T) {
	data := []byte(`{"targetVersions":{},"clusters":[{"name":"ordinary","nodes":[]}]}`)
	handled, err := NewFormatter(false).formatAgentsList(data, agentsOutputDef())
	if err != nil {
		t.Fatalf("shape probe returned an error: %v", err)
	}
	if handled {
		t.Fatal("the agents renderer claimed a response without a cluster agent")
	}
}

func TestAgentsListJSONKeepsServerDocument(t *testing.T) {
	raw := readAgentsFixture(t, "states.json")
	out := renderAgentsFixture(t, "states.json", true)
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("the JSON path changed the server document")
	}
	for _, field := range []string{
		`"agentState"`, `"agentStateLabel"`, `"agentStateDetail"`,
		`"updateActionAllowed"`, `"updateActionReason"`, `"updateAvailable"`,
	} {
		if !strings.Contains(out, field) {
			t.Errorf("the JSON path dropped %s", field)
		}
	}
}

func TestAgentsListDistinguishesEmptyReasonFromMissingReason(t *testing.T) {
	out := renderAgentsFixture(t, "states.json", false)
	allowed := lineContaining(out, "node-current")
	if !strings.HasSuffix(strings.TrimSpace(allowed), "-") {
		t.Errorf("an empty reason must render a dash: %s", allowed)
	}
	unreported := lineContaining(out, "node-unreported")
	if !strings.HasSuffix(strings.TrimSpace(unreported), absentMarker) {
		t.Errorf("an absent reason must render the absent marker: %s", unreported)
	}
}

func TestAgentsRendererCarriesNoAgentVerdict(t *testing.T) {
	source, err := os.ReadFile("agents_view.go")
	if err != nil {
		t.Fatalf("read renderer source: %v", err)
	}
	text := string(source)
	for _, forbidden := range []string{
		`"up_to_date"`, `"update_available"`, `"ahead"`, `"unknown"`, `"offline"`,
		`"Up to date"`, `"Update available"`, `"Ahead of the advertised version"`,
		`"Version unknown"`, `"Offline"`, `["agentState"]`, "semver", "Compare(",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the renderer carries a server verdict or version comparison %q", forbidden)
		}
	}
	if !strings.Contains(text, `"agentStateLabel"`) {
		t.Error("the renderer does not read the server's display label")
	}
}
