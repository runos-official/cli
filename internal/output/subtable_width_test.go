package output

import (
	"strings"
	"testing"
)

// A nested sub-table must cap a long cell exactly as the top-level table does.
//
// MEASURED 2026-09-13 against a live machine's lifecycle history. One stored
// event carried a BMC error roughly 900 characters long. The column is sized to
// the widest cell, so EVERY row in the table was padded to 955 characters and
// wrapped twelve times in an 80-column terminal. The command exists to be read
// when something went wrong, which is precisely when a long error is present.
//
// The top-level array table already truncates with truncateCell. The nested
// sub-table did not, and nothing said so.
func TestIndentedSubTableTruncatesALongCell(t *testing.T) {
	long := strings.Repeat("x", 900)
	rows := []map[string]any{
		{"kind": "updated", "message": long},
		{"kind": "updated", "message": ""},
	}

	out := captureStdout(t, func() { printIndentedSubTable(rows, "  ") })

	if strings.Contains(out, long) {
		t.Fatal("the long cell was printed in full; it must be truncated like the top-level table")
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if len([]rune(line)) > 120 {
			t.Fatalf("a row is %d characters wide, which wraps in any terminal: %.80s...", len([]rune(line)), line)
		}
	}
	if !strings.Contains(out, "...") {
		t.Fatal("a truncated cell must show that it was cut")
	}
}

// Short values must be untouched, so the ordinary table looks the same as before.
func TestIndentedSubTableLeavesShortCellsAlone(t *testing.T) {
	rows := []map[string]any{{"kind": "updated", "state": "allocated"}}

	out := captureStdout(t, func() { printIndentedSubTable(rows, "  ") })

	if !strings.Contains(out, "allocated") {
		t.Fatalf("a short value must survive intact, got:\n%s", out)
	}
	if strings.Contains(out, "...") {
		t.Fatalf("a short value must not be marked as cut, got:\n%s", out)
	}
}
