package output

import (
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

type agentsColumn struct {
	header string
	value  func(map[string]any) string
}

var agentsNodeColumns = []agentsColumn{
	{"NODE", func(node map[string]any) string { return agentsField(node, "name") }},
	{"ROLE", agentsRole},
	{"NODE STATUS", func(node map[string]any) string { return agentsField(node, "status") }},
	{"AGENT STATE", func(node map[string]any) string { return agentsField(node, "agentStateLabel") }},
	{"AGENT VERSION", func(node map[string]any) string { return agentsField(node, "nodeAgentVersion") }},
	{"UPDATE ALLOWED", func(node map[string]any) string { return agentsField(node, "updateActionAllowed") }},
	{"WHY NOT", agentsActionReason},
}

// formatAgentsList renders the account agent inventory. The response
// owns every state word and action decision. This client only labels
// columns and prints the values that the server supplied.
func (f *Formatter) formatAgentsList(data []byte, _ *manifest.Output) (bool, error) {
	envelope, ok := decodeJSONObject(data)
	if !ok {
		return false, nil
	}
	clusters, ok := agentsClusters(envelope)
	if !ok {
		return false, nil
	}

	targets, _ := envelope["targetVersions"].(map[string]any)
	fmt.Printf("Target versions: node agent %s, cluster agent %s\n",
		agentsField(targets, "nodeAgent"), agentsField(targets, "clusterAgent"))
	for _, cluster := range clusters {
		printAgentsCluster(cluster)
	}
	return true, nil
}

func agentsClusters(envelope map[string]any) ([]map[string]any, bool) {
	if _, ok := envelope["targetVersions"].(map[string]any); !ok {
		return nil, false
	}
	raw, ok := envelope["clusters"].([]any)
	if !ok {
		return nil, false
	}
	clusters := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		cluster, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := cluster["clusterAgent"].(map[string]any); !ok {
			return nil, false
		}
		if _, ok := cluster["nodes"].([]any); !ok {
			return nil, false
		}
		clusters = append(clusters, cluster)
	}
	return clusters, true
}

func printAgentsCluster(cluster map[string]any) {
	fmt.Printf("\nCluster %s (%s)\n", agentsField(cluster, "name"), agentsField(cluster, "cid"))
	fmt.Printf("  Cluster status: %s\n", agentsField(cluster, "status"))
	if status, _ := cluster["status"].(string); status != "ok" {
		fmt.Printf("  Message: %s\n", agentsField(cluster, "message"))
	}

	agent := cluster["clusterAgent"].(map[string]any)
	fmt.Printf("  Cluster agent: state %s | version %s | update allowed %s\n",
		agentsField(agent, "agentStateLabel"), agentsField(agent, "version"),
		agentsField(agent, "updateActionAllowed"))
	fmt.Printf("  Cluster agent detail: %s\n", agentsField(agent, "agentStateDetail"))
	if reason := agentsActionReason(agent); reason != "-" {
		fmt.Printf("  Cluster agent why not: %s\n", reason)
	}

	nodes := agentsNodes(cluster)
	if len(nodes) == 0 {
		fmt.Println("\n  This cluster reports no nodes.")
		return
	}
	fmt.Println()
	printAgentsNodeTable(nodes)
	for _, node := range nodes {
		fmt.Printf("  %s agent detail: %s\n",
			agentsField(node, "name"), agentsField(node, "agentStateDetail"))
	}
}

func agentsNodes(cluster map[string]any) []map[string]any {
	raw := cluster["nodes"].([]any)
	nodes := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if node, ok := item.(map[string]any); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func printAgentsNodeTable(nodes []map[string]any) {
	rows := make([][]string, 0, len(nodes))
	widths := make([]int, len(agentsNodeColumns))
	for i, column := range agentsNodeColumns {
		widths[i] = len(column.header)
	}
	for _, node := range nodes {
		row := make([]string, len(agentsNodeColumns))
		for i, column := range agentsNodeColumns {
			row[i] = column.value(node)
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
		rows = append(rows, row)
	}

	printAgentsTableLine(agentsHeaders(), widths)
	fmt.Println("  " + strings.Repeat("-", agentsTableWidth(widths)))
	for _, row := range rows {
		printAgentsTableLine(row, widths)
	}
}

func agentsHeaders() []string {
	headers := make([]string, len(agentsNodeColumns))
	for i, column := range agentsNodeColumns {
		headers[i] = column.header
	}
	return headers
}

func printAgentsTableLine(cells []string, widths []int) {
	var line strings.Builder
	line.WriteString("  ")
	for i, cell := range cells {
		if i > 0 {
			line.WriteString("  ")
		}
		fmt.Fprintf(&line, "%-*s", widths[i], cell)
	}
	fmt.Println(strings.TrimRight(line.String(), " "))
}

func agentsTableWidth(widths []int) int {
	total := 0
	for _, width := range widths {
		total += width
	}
	return total + (len(widths)-1)*2
}

func agentsField(obj map[string]any, key string) string {
	value, present := obj[key]
	if !present {
		return absentMarker
	}
	return maintenanceValue(value)
}

func agentsActionReason(obj map[string]any) string {
	value, present := obj["updateActionReason"]
	if !present || value == nil {
		return absentMarker
	}
	if text, ok := value.(string); ok && text == "" {
		return "-"
	}
	return maintenanceValue(value)
}

func agentsRole(node map[string]any) string {
	cp, cpPresent := node["isCp"].(bool)
	worker, workerPresent := node["isWorker"].(bool)
	if !cpPresent || !workerPresent {
		return absentMarker
	}
	switch {
	case cp && worker:
		return "control plane + worker"
	case cp:
		return "control plane"
	case worker:
		return "worker"
	default:
		return "-"
	}
}
