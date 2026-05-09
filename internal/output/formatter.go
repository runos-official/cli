// Package output formats API responses for terminal display.
package output

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/runos-official/cli/internal/manifest"
)

// Formatter formats command output
type Formatter struct {
	jsonOutput bool
}

// NewFormatter creates a new output formatter
func NewFormatter(jsonOutput bool) *Formatter {
	return &Formatter{jsonOutput: jsonOutput}
}

// Format formats and prints the response
func (f *Formatter) Format(data []byte, outputDef *manifest.Output) error {
	if f.jsonOutput {
		// Pretty print JSON
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			// Not valid JSON, print as-is
			fmt.Println(string(data))
			return nil
		}
		pretty, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(pretty))
		return nil
	}

	// Plain text output
	if outputDef == nil {
		fmt.Println(string(data))
		return nil
	}

	// FieldNames extracts just the names from the manifest's output
	// field schema (which can be a mix of bare strings and richer
	// objects). The formatter only uses names for table columns.
	switch outputDef.Type {
	case "array":
		return f.formatArray(data, outputDef.FieldNames())
	case "object":
		return f.formatObject(data, outputDef.FieldNames())
	default:
		fmt.Println(string(data))
	}

	return nil
}

func (f *Formatter) formatArray(data []byte, fields []string) error {
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		fmt.Println(string(data))
		return nil
	}

	if len(items) == 0 {
		fmt.Println("No items found")
		return nil
	}

	// Log-shape stream: pod logs come back as []PodLogEntry with the
	// shape {timestamp, podName, containerName, message}. A wide table
	// renders the message column at thousands of chars and breaks the
	// terminal. Recognise the shape and stream one entry per line as
	// `<timestamp> [<pod>] <message>` (mirroring kubectl's --timestamps
	// --prefix output). Drops to the table renderer for everything
	// else, and `--json` opt-in still gives the full structured form.
	if isLogShape(items) {
		streamLogEntries(items)
		return nil
	}

	// Determine which fields to show
	if len(fields) == 0 {
		// Use all keys from first item
		for k := range items[0] {
			fields = append(fields, k)
		}
		sort.Strings(fields)
	}

	// Calculate column widths (use display name without dots for header)
	widths := make([]int, len(fields))
	displayNames := make([]string, len(fields))
	for i, field := range fields {
		// Use the last part of dot notation as display name (e.g., "flags.systemInstance" -> "systemInstance")
		parts := strings.Split(field, ".")
		displayNames[i] = parts[len(parts)-1]
		widths[i] = len(displayNames[i])
	}
	for _, item := range items {
		for i, field := range fields {
			val := formatCellValue(getNestedValue(item, field))
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	header := ""
	for i := range fields {
		header += fmt.Sprintf("%-*s  ", widths[i], strings.ToUpper(displayNames[i]))
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Print rows
	for _, item := range items {
		row := ""
		for i, field := range fields {
			val := formatCellValue(getNestedValue(item, field))
			row += fmt.Sprintf("%-*s  ", widths[i], val)
		}
		fmt.Println(row)
	}

	return nil
}

func (f *Formatter) formatObject(data []byte, fields []string) error {
	var item map[string]any
	if err := json.Unmarshal(data, &item); err != nil {
		fmt.Println(string(data))
		return nil
	}

	// Determine which fields to show
	if len(fields) == 0 {
		for k := range item {
			fields = append(fields, k)
		}
		sort.Strings(fields)
	}

	// Find max key length for alignment (use display name without dots)
	displayNames := make([]string, len(fields))
	maxLen := 0
	for i, field := range fields {
		parts := strings.Split(field, ".")
		displayNames[i] = parts[len(parts)-1]
		if len(displayNames[i]) > maxLen {
			maxLen = len(displayNames[i])
		}
	}

	// Print key-value pairs
	for i, field := range fields {
		val := formatValue(getNestedValue(item, field))
		fmt.Printf("%-*s: %s\n", maxLen, displayNames[i], val)
	}

	return nil
}

func formatValue(v any) string {
	return formatValueWithIndent(v, 0)
}

// isLogShape reports whether items look like pod log entries (each
// item carries a timestamp + a message-ish field). All items must
// match for the stream to fire; partial matches keep the regular
// table rendering, since a single off-shape row would lose data.
func isLogShape(items []map[string]any) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		_, hasTs := it["timestamp"]
		_, hasMsg := it["message"]
		if !hasTs || !hasMsg {
			return false
		}
	}
	return true
}

// streamLogEntries prints each log entry on its own line as
// `<timestamp> [<pod>] <message>`. Container name is dropped from the
// default render to keep lines short; `--json` exposes the full
// structure for tooling that needs it.
func streamLogEntries(items []map[string]any) {
	for _, it := range items {
		ts := formatValue(it["timestamp"])
		pod := formatValue(it["podName"])
		msg := formatValue(it["message"])
		if pod != "" {
			fmt.Printf("%s [%s] %s\n", ts, pod, msg)
		} else {
			fmt.Printf("%s %s\n", ts, msg)
		}
	}
}

// formatCellValue is the table-cell variant of formatValue. It
// summarises arrays-of-objects (e.g. the `logs` column of `runos apps
// builds`) as `[N entries]` rather than concatenating every nested
// object inline. The pre-fix behaviour produced ~1000-char cells with
// `key=value, key=value; key=value, ...` runs that broke the table
// layout and dwarfed the columns the user actually wanted to read.
// Full structured detail is still available via `--json`.
func formatCellValue(v any) string {
	if items, ok := v.([]any); ok && len(items) > 0 {
		allObjects := true
		for _, it := range items {
			if _, isObj := it.(map[string]any); !isObj {
				allObjects = false
				break
			}
		}
		if allObjects {
			label := "entries"
			if len(items) == 1 {
				label = "entry"
			}
			return fmt.Sprintf("[%d %s]", len(items), label)
		}
	}
	return formatValueWithIndent(v, 0)
}

// getNestedValue retrieves a value from a map using dot notation (e.g., "flags.systemInstance")
func getNestedValue(item map[string]any, field string) any {
	parts := strings.Split(field, ".")
	var current any = item

	for _, part := range parts {
		if m, ok := current.(map[string]any); ok {
			current = m[part]
		} else {
			return nil
		}
	}
	return current
}

func formatValueWithIndent(v any, indent int) string {
	if v == nil {
		return ""
	}

	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case []any:
		return formatArray(val, indent)
	case map[string]any:
		return formatNestedObject(val, indent)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// formatArray formats an array of items intelligently
func formatArray(items []any, indent int) string {
	if len(items) == 0 {
		return ""
	}

	// Check if all items are objects
	allObjects := true
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			allObjects = false
			break
		}
	}

	if allObjects {
		// Format each object inline, separated by semicolons
		var parts []string
		for _, item := range items {
			obj := item.(map[string]any)
			parts = append(parts, formatNestedObject(obj, indent))
		}
		return strings.Join(parts, "; ")
	}

	// Simple array - join with commas
	strs := make([]string, len(items))
	for i, item := range items {
		strs[i] = formatValueWithIndent(item, indent)
	}
	return strings.Join(strs, ", ")
}

// formatNestedObject formats a nested object intelligently
func formatNestedObject(obj map[string]any, indent int) string {
	if len(obj) == 0 {
		return ""
	}

	// Check for common patterns and format them nicely

	// Pattern: network access entry (has link, name, type)
	if link, hasLink := obj["link"]; hasLink {
		name, hasName := obj["name"]
		if hasName {
			return fmt.Sprintf("%s: %s", formatValueWithIndent(name, indent), formatValueWithIndent(link, indent))
		}
		return formatValueWithIndent(link, indent)
	}

	// Pattern: status object (has state, message)
	if state, hasState := obj["state"]; hasState {
		msg, hasMsg := obj["message"]
		if hasMsg && msg != "" {
			return fmt.Sprintf("%s (%s)", formatValueWithIndent(state, indent), formatValueWithIndent(msg, indent))
		}
		return formatValueWithIndent(state, indent)
	}

	// Pattern: replicas info (has desired, ready, available)
	if desired, hasDesired := obj["desired"]; hasDesired {
		ready, hasReady := obj["ready"]
		available, hasAvailable := obj["available"]
		if hasReady && hasAvailable {
			return fmt.Sprintf("%v/%v ready, %v available",
				formatValueWithIndent(ready, indent),
				formatValueWithIndent(desired, indent),
				formatValueWithIndent(available, indent))
		}
	}

	// Default: show all keys as key=value pairs inline
	var parts []string
	for k, v := range obj {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatValueWithIndent(v, indent)))
	}
	return strings.Join(parts, ", ")
}
