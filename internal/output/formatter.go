// Package output formats API responses for terminal display.
package output

import (
	"bytes"
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

// unwrapArrayEnvelope returns the inner array bytes when `data` is a
// single-key JSON object whose value is itself an array; otherwise
// returns `data` unchanged. Pure shape detection: the envelope key
// doesn't need to be known in advance, so the helper stays correct
// for any list endpoint conductor wraps without a CLI release.
//
// I26-U / I26-O follow-up: list-style endpoints (apps_list, jobs_list,
// services_postgresql_users, ...) migrated to envelope responses
// (`{apps: [...]}`, `{jobs: [...]}`, `{users: [...]}`) while the
// manifest still declares `output.type: "array"`. Without this hook
// the text-mode formatter fell back to dumping raw JSON because
// `json.Unmarshal(envelope, &[]map[string]any)` failed.
func unwrapArrayEnvelope(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return data
	}
	if len(probe) != 1 {
		return data
	}
	for _, inner := range probe {
		innerTrim := bytes.TrimSpace(inner)
		if len(innerTrim) > 0 && innerTrim[0] == '[' {
			return inner
		}
	}
	return data
}

// pickArrayFromMultiKeyEnvelope handles the multi-key sibling of
// unwrapArrayEnvelope: when the response is `{primary:[...], aux:[...]}`
// (e.g. `services/postgresql/{id}/users` returns
// `{users:[{username, databases}], orphanSecretsDetected:[]}`), the
// single-key unwrap leaves the data as an object and the formatter falls
// back to raw JSON. This helper picks the array whose elements carry at
// least one of the manifest's declared field names, since the manifest
// declared `type: "array"` and named those fields specifically for that
// list. Empty arrays cannot be disambiguated by field membership, so on
// a tie the helper prefers the longest array, then breaks remaining ties
// by lexicographic key order for determinism. Returns data unchanged
// when the input is not an object, when no top-level field is an array,
// or when no manifest fields are declared (in which case the legacy
// single-key path is the only safe inference).
func pickArrayFromMultiKeyEnvelope(data []byte, fields []string) []byte {
	if len(fields) == 0 {
		return data
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return data
	}
	if len(probe) < 2 {
		return data
	}

	declared := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		// Manifest fields can be dotted (`flags.systemInstance`); match
		// on the top-level segment for membership against an item's keys.
		top := f
		if dot := strings.IndexByte(top, '.'); dot >= 0 {
			top = top[:dot]
		}
		declared[top] = struct{}{}
	}

	type candidate struct {
		key     string
		raw     json.RawMessage
		items   []map[string]any
		matches int
	}
	var arrays []candidate
	for k, v := range probe {
		vt := bytes.TrimSpace(v)
		if len(vt) == 0 || vt[0] != '[' {
			continue
		}
		c := candidate{key: k, raw: v}
		if err := json.Unmarshal(v, &c.items); err == nil {
			for _, it := range c.items {
				for itemKey := range it {
					if _, ok := declared[itemKey]; ok {
						c.matches++
					}
				}
			}
		}
		arrays = append(arrays, c)
	}
	if len(arrays) == 0 {
		return data
	}

	sort.Slice(arrays, func(i, j int) bool {
		if arrays[i].matches != arrays[j].matches {
			return arrays[i].matches > arrays[j].matches
		}
		if len(arrays[i].items) != len(arrays[j].items) {
			return len(arrays[i].items) > len(arrays[j].items)
		}
		return arrays[i].key < arrays[j].key
	})
	return arrays[0].raw
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
	// I26-U follow-up: conductor 16.0.0 wrapped list-style endpoints in
	// envelope objects (`{apps: [...]}`, `{jobs: [...]}`, etc.). The
	// manifest's `output.type: "array"` declaration didn't change in
	// the same release, so the bytes the formatter sees are an object
	// even though the schema says array. When the response is a
	// single-key object whose value is itself an array, unwrap it
	// before decoding. Pure shape detection so it stays correct for
	// any envelope key the conductor introduces without a CLI release.
	data = unwrapArrayEnvelope(data)
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		// Sibling path to unwrapArrayEnvelope: some list endpoints
		// (services/postgresql/{id}/users, for example) return a
		// multi-key envelope where the primary array sits beside an
		// auxiliary diagnostic field. The shape-keyed single-key
		// unwrap can't disambiguate, so the manifest's declared field
		// list is used to pick the array whose elements match.
		data = pickArrayFromMultiKeyEnvelope(data, fields)
		if err2 := json.Unmarshal(data, &items); err2 != nil {
			fmt.Println(string(data))
			return nil
		}
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
	// Pre-truncate every cell so a single outsized value (e.g. a
	// ~250-char api-key NAME) cannot push every other column hundreds
	// of chars right. The cap applies in text mode only; --json still
	// emits the full untruncated value so machine consumers keep the
	// raw data. Truncated cells render as `<first N-3 chars>...` and
	// the width calculation honours the truncated string.
	cells := make([][]string, len(items))
	for r, item := range items {
		row := make([]string, len(fields))
		for i, field := range fields {
			val := truncateCell(formatCellValue(getNestedValue(item, field)), maxTextCellWidth)
			row[i] = val
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
		cells[r] = row
	}

	// Print header
	header := ""
	for i := range fields {
		header += fmt.Sprintf("%-*s  ", widths[i], headers[i])
	}
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", len(header)))

	// Print rows
	for _, row := range cells {
		line := ""
		for i, val := range row {
			line += fmt.Sprintf("%-*s  ", widths[i], val)
		}
		fmt.Println(line)
	}

	return nil
}

// maxTextCellWidth caps any single cell in a top-level array table at
// 40 runes. Names, descriptions, and uid strings have surfaced in the
// wild at 250+ chars and a single such cell pushes every subsequent
// column hundreds of chars right, making the whole table unreadable
// without scrolling. 40 is wide enough for typical resource names
// (`prod-billing-worker-staging`) but narrow enough that a pathological
// outlier can't dominate. Full values stay available via --json.
const maxTextCellWidth = 40

// truncateCell returns val unchanged when it fits within max display
// runes, otherwise returns the first max-3 runes followed by `...`.
// Counts runes (not bytes) so a string with multi-byte characters
// truncates at a visible-character boundary instead of mid-rune. Pure
// helper so the regression test can exercise short / exact / over /
// multi-byte inputs without a Formatter dance.
//
// URL-shaped values (http:// or https:// prefix) bypass the cap: those
// are typically the primary value the user came for (apps
// network-access prints endpoints, services dependencies prints
// targetIngressUrl), and copy-paste from the terminal needs the full
// string. The table renderer's width calc picks up the longer cell so
// alignment still works for downstream columns. Issue 106.
func truncateCell(val string, max int) string {
	if max <= 3 {
		return val
	}
	if isURLValue(val) {
		return val
	}
	runes := []rune(val)
	if len(runes) <= max {
		return val
	}
	return string(runes[:max-3]) + "..."
}

// isURLValue reports whether val is an http(s) URL the user is likely
// to copy-paste. Cheap prefix check; no full URL parsing because we
// just need to recognise the "primary value" cells in the table.
func isURLValue(val string) bool {
	return strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://")
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
	} else {
		// I24-D: forward-compat for server-side response extensions.
		// When the manifest declared fields but the live response carries
		// MORE top-level keys than the manifest knows about (canonical
		// case: conductor 14.2.0 added `gitlabRunner: {...}` to
		// `integrations/gitlab-runner/status` without a CLI release),
		// append the new keys at the end instead of silently dropping
		// them. The manifest's declared order still drives the primary
		// column layout; unknown keys land alphabetically after. Pairs
		// with the I15-C error-envelope pass-through doctrine.
		declared := make(map[string]bool, len(fields))
		for _, f := range fields {
			declared[strings.SplitN(f, ".", 2)[0]] = true
		}
		var unknown []string
		for k := range item {
			if !declared[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			fields = append(fields, unknown...)
		}
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
		// Skip declared-but-absent top-level fields so text output
		// matches --json (which omits keys the response didn't carry).
		// Pre-fix, `runos jobs show <id>` rendered blank `createdAt :`
		// and `error :` rows because the manifest declared those fields
		// but the API response omitted them. Nested paths
		// (`flags.systemInstance`) keep the prior render-as-blank
		// behaviour: presence detection on a missing intermediate
		// segment is ambiguous and the legacy rendering of an empty
		// nested cell was rarely confusing in practice.
		if !strings.Contains(field, ".") {
			if _, present := item[field]; !present {
				continue
			}
		}
		raw := getNestedValue(item, field)
		// A17: an empty array rendered as a blank cell, which reads as
		// "the server did not answer" rather than "there are none". The
		// non-empty case already says "N entries", so say "0 entries".
		if arr, ok := raw.([]any); ok && len(arr) == 0 {
			fmt.Printf("%-*s: 0 entries\n", maxLen, displayNames[i])
			continue
		}
		if rows, ok := arrayOfObjects(raw); ok && len(rows) > 0 {
			label := "entries"
			if len(rows) == 1 {
				label = "entry"
			}
			fmt.Printf("%-*s: %d %s\n", maxLen, displayNames[i], len(rows), label)
			printIndentedSubTable(rows, "  ")
			continue
		}
		// I24-D: nested-object values render as `<header>:\n  k: v\n  k: v\n`
		// (one inner key per line, alphabetised) instead of the single-line
		// `{key: value, key: value}` mash. Canonical case: the new
		// `gitlabRunner` block on `integrations/gitlab-runner/status`,
		// which carries 5-7 keys that should each be visible. Primitives,
		// arrays, and empty objects keep their one-line rendering.
		if obj, ok := raw.(map[string]any); ok && len(obj) > 0 {
			fmt.Printf("%-*s:\n", maxLen, displayNames[i])
			printIndentedObject(obj, "  ")
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
// printIndentedObject renders a single-level map as `<indent>k: v` per
// key, alphabetised. Nested objects recurse with deeper indent; nested
// arrays render via formatValue (one-line); arrays-of-objects fall
// through to the existing array sub-table renderer. Used by formatObject
// when a top-level field's value is itself a map so the user sees the
// inner keys instead of a one-line mash. Regression target: I24-D.
func printIndentedObject(obj map[string]any, indent string) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	maxKey := 0
	for _, k := range keys {
		if len(k) > maxKey {
			maxKey = len(k)
		}
	}
	for _, k := range keys {
		v := obj[k]
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			fmt.Printf("%s%-*s:\n", indent, maxKey, k)
			printIndentedObject(nested, indent+"  ")
			continue
		}
		if rows, ok := arrayOfObjects(v); ok && len(rows) > 0 {
			label := "entries"
			if len(rows) == 1 {
				label = "entry"
			}
			fmt.Printf("%s%-*s: %d %s\n", indent, maxKey, k, len(rows), label)
			printIndentedSubTable(rows, indent+"  ")
			continue
		}
		fmt.Printf("%s%-*s: %s\n", indent, maxKey, k, formatValue(v))
	}
}

func printIndentedSubTable(rows []map[string]any, indent string) {
	// A18: `vm-usage` puts per-VM rows in this sub-table and each row
	// carries `segments` and `shapeSeconds` arrays of objects, which
	// collapsed to `[1 entry]` and hid the only numbers the report
	// exists to deliver. Render such rows as blocks, where
	// printIndentedObject can recurse into them.
	if rowsCarryNestedStructure(rows) {
		for _, r := range rows {
			printIndentedObject(r, indent)
			fmt.Println()
		}
		return
	}
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
		// `apps_list` / `apps_show` declare a flat `port` field in the
		// manifest but the response carries `servicePortMappings: [{port,
		// standardHttps, ...}]` instead, so the formatter previously
		// rendered the PORT column blank for every row. Derive the
		// displayed value from the array when the flat lookup misses: a
		// single mapping renders as that port number, multiple as a
		// comma-joined list (`3000,8080`). Returns nil if no usable
		// values were found so unrelated commands that legitimately have
		// no port surface keep their blank cell.
		if parts[0] == "port" {
			if derived := derivePortFromServicePortMappings(item); derived != nil {
				return derived
			}
		}
	}
	return current
}

// derivePortFromServicePortMappings extracts a printable port value
// from the `servicePortMappings` array on an apps_list / apps_show
// response. Returns the bare port number when there's exactly one
// mapping, a comma-joined string when there are several, or nil when
// the array is absent / empty / carries no usable port values. Pure
// helper so the regression test can exercise the shape variants
// (missing key, empty array, single, multiple, non-numeric) without
// going through getNestedValue.
func derivePortFromServicePortMappings(item map[string]any) any {
	raw, ok := item["servicePortMappings"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	var ports []string
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		val, ok := m["port"]
		if !ok || val == nil {
			continue
		}
		s := formatValueWithIndent(val, 0)
		if s == "" {
			continue
		}
		ports = append(ports, s)
	}
	if len(ports) == 0 {
		return nil
	}
	if len(ports) == 1 {
		return ports[0]
	}
	return strings.Join(ports, ",")
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

	// Default: show all keys as key=value pairs inline, alphabetised
	// so the output is deterministic across runs. Pre-fix the default
	// branch iterated `for k := range obj` which is Go-spec non-
	// deterministic, making `agents list` render different field orders
	// on consecutive invocations and breaking screenshot/diff-based
	// comparisons. JSON output is already alphabetical, so text now
	// matches that contract.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatValueWithIndent(obj[k], indent)))
	}
	return strings.Join(parts, ", ")
}
