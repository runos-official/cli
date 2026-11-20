package output

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/manifest"
)

// Story 343 / FCR2030, finding 1250 point 2: formatObject and
// formatArray used plain json.Unmarshal (no UseNumber), unlike
// Format's own --json branch. A number outside float64's exact range
// made ONLY these text-mode decodes fail (strconv.ParseFloat
// ErrRange), falling back to printing the raw, unformatted body
// instead of the normal per-field table render -- a divergence a
// caller validating "will this decode the way the renderer renders
// it" could not see from outside this package.
func hugeNumberLiteral() string {
	return "9" + strings.Repeat("9", 400)
}

func TestFormatObject_LargeNumberDoesNotFallBackToRawPrint(t *testing.T) {
	body := []byte(`{"id":"k1","quota":` + hugeNumberLiteral() + `}`)
	f := NewFormatter(false)
	got := captureStdout(t, func() {
		if err := f.Format(body, &manifest.Output{Type: "object"}); err != nil {
			t.Fatalf("Format: %v", err)
		}
	})
	if strings.Contains(got, "{") {
		t.Errorf("expected the normal key: value table render, not a raw JSON dump, got: %q", got)
	}
	if !strings.Contains(got, "id") || !strings.Contains(got, "k1") {
		t.Errorf("expected the id field to render, got: %q", got)
	}
	if !strings.Contains(got, hugeNumberLiteral()) {
		t.Errorf("expected the exact large number digits, got: %q", got)
	}
}

func TestFormatArray_LargeNumberDoesNotFallBackToRawPrint(t *testing.T) {
	body := []byte(`[{"id":"k1","quota":` + hugeNumberLiteral() + `}]`)
	f := NewFormatter(false)
	got := captureStdout(t, func() {
		if err := f.Format(body, &manifest.Output{Type: "array"}); err != nil {
			t.Fatalf("Format: %v", err)
		}
	})
	// A raw-print fallback would dump the bare JSON (starting with `[`,
	// no header row). The table render prints a header row and truncates
	// a very wide cell, so check for the table shape and the number's
	// leading digits rather than the full 400-digit literal.
	if strings.HasPrefix(strings.TrimSpace(got), "[") {
		t.Errorf("expected the normal table render, not a raw JSON dump, got: %q", got)
	}
	if !strings.Contains(got, "k1") || !strings.Contains(got, "QUOTA") {
		t.Errorf("expected the id and quota columns to render, got: %q", got)
	}
	if !strings.Contains(got, "999999999") {
		t.Errorf("expected the large number's leading digits, got: %q", got)
	}
}

// formatValueWithIndent must render a json.Number by its exact original
// digit text, never by rounding it through float64 first.
func TestFormatValueWithIndent_JSONNumberRendersExactDigits(t *testing.T) {
	cases := []string{"42", "3.140", hugeNumberLiteral()}
	for _, c := range cases {
		if got := formatValueWithIndent(json.Number(c), 0); got != c {
			t.Errorf("formatValueWithIndent(%q) = %q, want %q", c, got, c)
		}
	}
}
