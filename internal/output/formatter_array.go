package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// decodeArrayOfObjects decodes data the same way Format's --json branch
// and formatObject do (finding 1250 point 2): a streaming decoder with
// UseNumber, never json.Unmarshal, so a number outside float64's range
// cannot make only this decode fail while the other renderers for the
// same bytes succeed.
func decodeArrayOfObjects(data []byte) ([]map[string]any, error) {
	var items []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err := decoder.Decode(&items)
	return items, err
}

func (f *Formatter) formatArray(data []byte, fields []string) error {
	// I26-U follow-up: conductor 16.0.0 wrapped list-style endpoints in
	// envelope objects while the manifest still declared an array.
	data = unwrapArrayEnvelope(data)
	items, err := decodeArrayOfObjects(data)
	if err != nil {
		data = pickArrayFromMultiKeyEnvelope(data, fields)
		items, err = decodeArrayOfObjects(data)
		if err != nil {
			fmt.Println(string(data))
			return nil
		}
	}

	if len(items) == 0 {
		fmt.Println("No items found")
		return nil
	}

	if isLogShape(items) {
		streamLogEntries(items)
		return nil
	}

	// I24-D's array sibling (Story 343 / FCR2030, review findings 1099/1108):
	// formatObject already appends a key the manifest did not declare instead
	// of silently dropping it; this had no equivalent, so a server that adds
	// a column to a LIST response (Conductor's additive credentialType,
	// custodialAccountId, authorityAccountId, permissionSource and
	// legacyClassification on account/api-keys/list, story 331) rendered the
	// declared columns only and dropped every new one, in text mode. --json
	// is unaffected: Format pretty-prints the raw body regardless.
	//
	// Scanned across every row, not just the first: different rows can carry
	// different additive keys (Conductor's serialize() sets
	// authorityAccountId on a personal key and custodialAccountId on an
	// account-role key, never both), and a key only the second row has must
	// still get a column.
	if len(fields) == 0 {
		for k := range items[0] {
			fields = append(fields, k)
		}
		sort.Strings(fields)
	} else {
		declared := make(map[string]bool, len(fields))
		for _, f := range fields {
			declared[strings.SplitN(f, ".", 2)[0]] = true
		}
		unknownSet := make(map[string]bool)
		for _, item := range items {
			for k := range item {
				if !declared[k] {
					unknownSet[k] = true
				}
			}
		}
		if len(unknownSet) > 0 {
			unknown := make([]string, 0, len(unknownSet))
			for k := range unknownSet {
				unknown = append(unknown, k)
			}
			sort.Strings(unknown)
			fields = append(fields, unknown...)
		}
	}

	widths := make([]int, len(fields))
	headers := make([]string, len(fields))
	for i, field := range fields {
		parts := strings.Split(field, ".")
		displayName := parts[len(parts)-1]
		headers[i] = headerLabel(displayName)
		widths[i] = len(headers[i])
	}
	cells := make([][]string, len(items))
	for rowIndex, item := range items {
		row := make([]string, len(fields))
		for fieldIndex, field := range fields {
			value := truncateCell(formatCellValue(getNestedValue(item, field)), maxTextCellWidth)
			row[fieldIndex] = value
			if len(value) > widths[fieldIndex] {
				widths[fieldIndex] = len(value)
			}
		}
		cells[rowIndex] = row
	}

	var header strings.Builder
	for i := range fields {
		fmt.Fprintf(&header, "%-*s  ", widths[i], headers[i])
	}
	fmt.Println(header.String())
	fmt.Println(strings.Repeat("-", header.Len()))

	for _, row := range cells {
		var line strings.Builder
		for i, value := range row {
			fmt.Fprintf(&line, "%-*s  ", widths[i], value)
		}
		fmt.Println(line.String())
	}

	return nil
}

const maxTextCellWidth = 40

func truncateCell(value string, max int) string {
	if max <= 3 {
		return value
	}
	if isURLValue(value) {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-3]) + "..."
}

func isURLValue(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}
