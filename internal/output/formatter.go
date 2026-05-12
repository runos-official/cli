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
	headers := make([]string, len(fields))
	for i, field := range fields {
		// Use the last part of dot notation as display name (e.g., "flags.systemInstance" -> "systemInstance")
		parts := strings.Split(field, ".")
		displayNames[i] = parts[len(parts)-1]
		// I5-B: legacy field names like `__docId` map to a friendlier
		// header (`ID`) so the table doesn't render the raw Firestore
		// subdoc convention name.
		headers[i] = headerLabel(displayNames[i])
		widths[i] = len(headers[i])
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
		header += fmt.Sprintf("%-*s  ", widths[i], headers[i])
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

	// Print key-value pairs. Array-of-objects fields (e.g. `archives` on
	// `apps cli-archives`, `users` on a postgres `show`) used to render
	// inline as semicolon-mashed `k=v, k=v; k=v, ...` strings that broke
	// the terminal layout and forced consumers into `--json`. Render
	// them instead as `<N entries>` followed by an indented sub-table,
	// so the user sees both the count summary and the rows. Single
	// values and primitive arrays keep their old one-line rendering.
	// Regression target: I12-E.
	for i, field := range fields {
		raw := getNestedValue(item, field)
		if rows, ok := arrayOfObjects(raw); ok && len(rows) > 0 {
			label := "entries"
			if len(rows) == 1 {
				label = "entry"
			}
			fmt.Printf("%-*s: %d %s\n", maxLen, displayNames[i], len(rows), label)
			printIndentedSubTable(rows, "  ")
			continue
		}
		val := formatValue(raw)
		fmt.Printf("%-*s: %s\n", maxLen, displayNames[i], val)
	}

	return nil
}

// arrayOfObjects reports whether v is a non-empty `[]any` where every
// element is a `map[string]any`. Used to decide whether a field's value
// should render as a nested sub-table or as a single-line string.
func arrayOfObjects(v any) ([]map[string]any, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

// printIndentedSubTable renders a slice of homogeneous objects as a
// table prefixed with `indent` on every line. Column order is the union
// of keys across all rows, sorted alphabetically. Used by formatObject
// to render array-of-objects fields without semicolon-mashing.
func printIndentedSubTable(rows []map[string]any, indent string) {
	keys := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			keys[k] = true
		}
	}
	headers := make([]string, 0, len(keys))
	for k := range keys {
		headers = append(headers, k)
	}
	sort.Strings(headers)

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(strings.ToUpper(h))
	}
	for _, r := range rows {
		for i, h := range headers {
			val := formatCellValue(r[h])
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	headerLine := indent
	for i, h := range headers {
		headerLine += fmt.Sprintf("%-*s  ", widths[i], strings.ToUpper(h))
	}
	fmt.Println(strings.TrimRight(headerLine, " "))
	fmt.Println(indent + strings.Repeat("-", len(headerLine)-len(indent)))

	for _, r := range rows {
		row := indent
		for i, h := range headers {
			row += fmt.Sprintf("%-*s  ", widths[i], formatCellValue(r[h]))
		}
		fmt.Println(strings.TrimRight(row, " "))
	}
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

// fieldNameAliases maps legacy manifest output field names to their
// post-normalisation equivalents. The conductor's `apps_overrides`
// (and related) endpoints used to expose Firestore's `__docId`
// subdoc identifier as a field name; the response was later
// normalised to `id` (iter-1 11b). The manifest's `output.fields`
// list still carries the legacy `__docId` name in some commands,
// so the response body has `id` but the renderer is asked to look
// up `__docId` and finds nothing. Result: a literal `__DOCID`
// column header and an empty value column (I5-B).
//
// The alias map is the defensive CLI layer: when getNestedValue
// resolves the manifest-declared name and finds nothing, it retries
// against the alias before giving up. headerLabel does the matching
// thing for the column header so `__DOCID` displays as `ID`.
//
// Each entry is one-way (legacy → current) and case-sensitive. Add
// to the map when a future manifest output field renames; the alias
// keeps older CLI binaries rendering correctly until the user
// re-runs `runos manifest update`.
var fieldNameAliases = map[string]string{
	"__docId": "id",
}

// headerLabel returns the column-header text for a manifest output
// field name. Defaults to upper-casing the display name (the last
// dotted segment); when the field has an alias defined,
// upper-cases the alias instead so legacy names like `__docId`
// render as `ID` rather than `__DOCID`.
func headerLabel(displayName string) string {
	if alias, ok := fieldNameAliases[displayName]; ok {
		return strings.ToUpper(alias)
	}
	return strings.ToUpper(displayName)
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
	// I5-B: when a legacy field name (e.g. `__docId`) resolves to nil
	// because the response has been normalised to a new name (e.g.
	// `id`), retry against the alias before giving up. Only applies
	// to single-segment lookups; dotted nested paths fall through.
	if current == nil && len(parts) == 1 {
		if alias, ok := fieldNameAliases[parts[0]]; ok {
			if v, found := item[alias]; found {
				return v
			}
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
