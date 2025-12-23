package output

import (
	"encoding/json"
	"fmt"
	"strings"

	"cli/internal/manifest"
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
		var v interface{}
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
	var items []map[string]interface{}
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
	}

	// Calculate column widths
	widths := make([]int, len(fields))
	for i, field := range fields {
		widths[i] = len(field)
	}
	for _, item := range items {
		for i, field := range fields {
			val := formatValue(item[field])
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Print header
	header := ""
	for i, field := range fields {
		header += fmt.Sprintf("%-*s  ", widths[i], strings.ToUpper(field))
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Print rows
	for _, item := range items {
		row := ""
		for i, field := range fields {
			val := formatValue(item[field])
			row += fmt.Sprintf("%-*s  ", widths[i], val)
		}
		fmt.Println(row)
	}

	return nil
}

func (f *Formatter) formatObject(data []byte, fields []string) error {
	var item map[string]interface{}
	if err := json.Unmarshal(data, &item); err != nil {
		fmt.Println(string(data))
		return nil
	}

	// Determine which fields to show
	if len(fields) == 0 {
		for k := range item {
			fields = append(fields, k)
		}
	}

	// Find max key length for alignment
	maxLen := 0
	for _, field := range fields {
		if len(field) > maxLen {
			maxLen = len(field)
		}
	}

	// Print key-value pairs
	for _, field := range fields {
		val := formatValue(item[field])
		fmt.Printf("%-*s: %s\n", maxLen, field, val)
	}

	return nil
}

func formatValue(v interface{}) string {
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
	case []interface{}:
		strs := make([]string, len(val))
		for i, item := range val {
			strs[i] = formatValue(item)
		}
		return strings.Join(strs, ", ")
	case map[string]interface{}:
		// For nested objects, show as JSON
		data, _ := json.Marshal(val)
		return string(data)
	default:
		return fmt.Sprintf("%v", val)
	}
}
