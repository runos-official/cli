package desktopcontract

import (
	"fmt"
	"strings"
	"unicode"
)

// Limits for the text a failed command leaves on stderr. Desktop reads all of stderr after a failure
// and shows it in a menu or a small window, so the text is plain and short.
const (
	// MaxErrorLength is the longest error text allowed whole.
	MaxErrorLength = 300
	// MaxErrorLine is the longest line allowed when the text is longer than MaxErrorLength.
	MaxErrorLine = 120
)

// PlainErrorProblems lists why text is not fit to show in a Desktop menu: an ANSI escape or any
// control character other than a newline, whitespace around the text, nothing at all, or a length
// over MaxErrorLength whose lines are also over MaxErrorLine. It is empty when the text is fine.
func PlainErrorProblems(text string) []string {
	var out []string
	if strings.TrimSpace(text) == "" {
		return []string{"the text is empty"}
	}
	if text != strings.TrimSpace(text) {
		out = append(out, "the text has leading or trailing whitespace")
	}
	for i, r := range text {
		if r == '\x1b' {
			out = append(out, fmt.Sprintf("an ANSI escape at byte %d", i))
			break
		}
		if r != '\n' && unicode.IsControl(r) {
			out = append(out, fmt.Sprintf("control character %U at byte %d", r, i))
			break
		}
	}
	if len([]rune(text)) > MaxErrorLength {
		for _, line := range strings.Split(text, "\n") {
			if n := len([]rune(line)); n > MaxErrorLine {
				out = append(out, fmt.Sprintf("%d characters and a line of %d (limit %d, or lines up to %d)",
					len([]rune(text)), n, MaxErrorLength, MaxErrorLine))
				break
			}
		}
	}
	return out
}
