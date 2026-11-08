package output

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// absentMarker is what a cell prints when the server reported no value
// for it. An empty cell reads as nothing-wrong, and this view exists to
// remove that reading. The marker is bracketed and lower case, so no
// reader can take a gap for a word the server derived.
const absentMarker = "(not reported)"

// maintenanceColumn is one node-table column. The header is layout, so
// this client owns it. The value is the server's, so this client never
// maps it, defaults it or compares it.
type maintenanceColumn struct {
	header string
	key    string
}

// maintenanceNodeColumns fixes the node-table column order. The server
// manifest declares one output field for this command, `clusters`, so
// it names no node field and the order has to live here.
//
// Every value is printed as the server sent it. The two age columns
// carry seconds, which is the unit the server derives and sends, so the
// header names the unit and this client does no arithmetic.
var maintenanceNodeColumns = []maintenanceColumn{
	{"NODE", "name"},
	{"STATUS", "nodeMaintenanceStatusLabel"},
	{"LAST BOOT AGE (S)", "lastBootAgeSeconds"},
	{"REBOOT PENDING AGE (S)", "rebootPendingAgeSeconds"},
	{"SECURITY UPDATES", "securityUpdateCount"},
	{"LAST SUCCESSFUL RUN", "lastSuccessfulMaintenanceRunAt"},
	{"AGENT VERSION", "nodeAgentVersion"},
	{"AGENT STATE", "agentStateLabel"},
	{"AUTO UPDATE UNITS", "autoUpdateUnitState"},
}

// formatMaintenanceView renders the account maintenance read endpoint:
// one block per cluster, the cluster facts, a node table and a detail
// block per node. It reports false for every other response, so each
// existing command keeps the output it has today.
//
// The manifest output definition is not read. The entry declares the
// single field `clusters`, so it can neither name a column nor order
// one, and a branch that read it would never run.
func (f *Formatter) formatMaintenanceView(data []byte, _ *manifest.Output) (bool, error) {
	envelope, ok := decodeJSONObject(data)
	if !ok {
		return false, nil
	}
	clusters, ok := maintenanceClusters(envelope)
	if !ok {
		return false, nil
	}

	// The account-level facts, by the server's own field names. Every
	// key but the cluster array prints, so a field a later release adds
	// reaches the reader without a CLI change.
	printIndentedObject(markedMap(omitKeys(envelope, "clusters")), "")
	if len(clusters) == 0 {
		fmt.Println("\nThis account has no clusters.")
		return true, nil
	}
	for _, cluster := range clusters {
		printMaintenanceCluster(cluster)
	}
	return true, nil
}

// maintenanceClusters reports the cluster array when the response is
// this view's, and reports false otherwise.
//
// DETECTION IS BY SHAPE, never by the command path, which copies the
// teardown hook beside it. Two keys are required, and the second one is
// not optional: `clusters/list` also answers `{"clusters":[...]}`, and
// an EMPTY list from that command satisfies "every element carries a
// node array" on its own. The top-level `generatedAt` key, which this
// view carries and `clusters/list` does not, is what separates them.
func maintenanceClusters(envelope map[string]any) ([]map[string]any, bool) {
	if _, present := envelope["generatedAt"]; !present {
		return nil, false
	}
	raw, present := envelope["clusters"]
	if !present {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	clusters := make([]map[string]any, 0, len(list))
	for _, item := range list {
		cluster, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := cluster["nodes"].([]any); !ok {
			return nil, false
		}
		clusters = append(clusters, cluster)
	}
	return clusters, true
}

// printMaintenanceCluster prints one cluster block: the heading, the
// cluster facts, the node table, then one detail block per node.
func printMaintenanceCluster(cluster map[string]any) {
	fmt.Printf("\nCluster %s (%s)\n",
		maintenanceValue(cluster["name"]), maintenanceValue(cluster["cid"]))

	// The heading already carries the identity, and the node array has
	// its own table, so those three keys are the only ones left out.
	// Everything else prints under its server field name, including the
	// nested cluster agent, Kubernetes and automatic-update-unit facts.
	printIndentedObject(markedMap(omitKeys(cluster, "name", "cid", "nodes")), "  ")

	nodes := maintenanceNodes(cluster)
	if len(nodes) == 0 {
		fmt.Println("\n  This cluster reports no nodes.")
		return
	}

	fmt.Println()
	printMaintenanceNodeTable(nodes)
	for _, node := range nodes {
		fmt.Printf("\n  Node %s (%s)\n",
			maintenanceValue(node["name"]), maintenanceValue(node["nid"]))
		printIndentedObject(markedMap(omitKeys(node, maintenanceDetailOmitKeys()...)), "    ")
	}
}

func maintenanceNodes(cluster map[string]any) []map[string]any {
	list, _ := cluster["nodes"].([]any)
	nodes := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if node, ok := item.(map[string]any); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// printMaintenanceNodeTable prints the node rows as one aligned table.
// A column is sized to its widest cell, and a long cell truncates, the
// same way the generic array renderer treats a wide value.
func printMaintenanceNodeTable(nodes []map[string]any) {
	cells := make([][]string, 0, len(nodes))
	widths := make([]int, len(maintenanceNodeColumns))
	for i, column := range maintenanceNodeColumns {
		widths[i] = len(column.header)
	}
	for _, node := range nodes {
		row := make([]string, len(maintenanceNodeColumns))
		for i, column := range maintenanceNodeColumns {
			row[i] = truncateCell(maintenanceValue(getNestedValue(node, column.key)), maxTextCellWidth)
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
		cells = append(cells, row)
	}

	header := "  "
	for i, column := range maintenanceNodeColumns {
		header += fmt.Sprintf("%-*s  ", widths[i], column.header)
	}
	header = strings.TrimRight(header, " ")
	fmt.Println(header)
	fmt.Println("  " + strings.Repeat("-", len(header)-2))
	for _, row := range cells {
		line := "  "
		for i, cell := range row {
			line += fmt.Sprintf("%-*s  ", widths[i], cell)
		}
		fmt.Println(strings.TrimRight(line, " "))
	}
}

// maintenanceDetailOmitKeys names the node keys the table already
// shows, plus the identifier in the detail heading. The detail block
// prints every remaining key, so a field a later release adds shows up
// without a CLI change and no server fact is ever dropped.
func maintenanceDetailOmitKeys() []string {
	keys := make([]string, 0, len(maintenanceNodeColumns)+1)
	for _, column := range maintenanceNodeColumns {
		keys = append(keys, column.key)
	}
	return append(keys, "nid")
}

func omitKeys(obj map[string]any, keys ...string) map[string]any {
	drop := make(map[string]bool, len(keys))
	for _, key := range keys {
		drop[key] = true
	}
	kept := make(map[string]any, len(obj))
	for key, value := range obj {
		if !drop[key] {
			kept[key] = value
		}
	}
	return kept
}

// maintenanceValue renders one server value for display. An absent,
// null or empty value becomes the absent marker. A present value is
// rendered by the package's own value formatter, so this view prints a
// string, a number and a list the same way every other command does.
//
// This function holds no word list, no comparison and no default word:
// it either prints what the server sent or says nothing was reported.
func maintenanceValue(value any) string {
	if value == nil {
		return absentMarker
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return absentMarker
		}
	case []any:
		if len(typed) == 0 {
			return absentMarker
		}
	case map[string]any:
		if len(typed) == 0 {
			return absentMarker
		}
	case json.Number:
		return typed.String()
	}
	rendered := formatValueWithIndent(value, 0)
	if strings.TrimSpace(rendered) == "" {
		return absentMarker
	}
	return rendered
}

// markedMap rewrites a decoded response so every leaf carries a
// printable value, and the absent marker stands where the server
// reported nothing. The block renderers print an empty string for a
// null leaf, which is the blank cell this view must never show.
func markedMap(obj map[string]any) map[string]any {
	marked := make(map[string]any, len(obj))
	for key, value := range obj {
		marked[key] = markedValue(value)
	}
	return marked
}

func markedValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return absentMarker
		}
		return markedMap(typed)
	case []any:
		if len(typed) == 0 {
			return absentMarker
		}
		marked := make([]any, 0, len(typed))
		for _, item := range typed {
			marked = append(marked, markedValue(item))
		}
		return marked
	}
	return maintenanceValue(value)
}
