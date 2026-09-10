package output

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func (f *Formatter) formatArray(data []byte, fields []string) error {
	// I26-U follow-up: conductor 16.0.0 wrapped list-style endpoints in
	// envelope objects while the manifest still declared an array.
	data = unwrapArrayEnvelope(data)
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
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

	if isLogShape(items) {
		streamLogEntries(items)
		return nil
	}

	if len(fields) == 0 {
		for k := range items[0] {
			fields = append(fields, k)
		}
		sort.Strings(fields)
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
