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

	switch outputDef.Type {
	case "array":
		return f.formatArray(data, outputDef.Fields)
	case "object":
		return f.formatObject(data, outputDef.Fields)
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
			val := formatValue(getNestedValue(item, field))
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
			val := formatValue(getNestedValue(item, field))
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
