package envfile

import (
	"reflect"
	"strings"
	"testing"
)

// Issue 73: round-tripping env values through `runos apps pull` ->
// edit -> `runos apps sync` used to silently mangle every value that
// carried a newline, leading/trailing whitespace, or a quote character.
// This test pins the lossless property by encoding then decoding each
// pathological value and comparing byte-for-byte.
func TestFormatParseRoundTrip(t *testing.T) {
	cases := map[string]string{
		"PLAIN":         "simple",
		"EMPTY":         "",
		"NEWLINE":       "line1\nline2\nline3",
		"CRLF":          "a\r\nb",
		"TAB":           "before\tafter",
		"DOUBLE_QUOTE":  `she said "hi"`,
		"SINGLE_QUOTE":  `he said 'yo'`,
		"MIXED_QUOTES":  `mix: "yo" 'sup' "bye"`,
		"LEAD_SPACE":    "   leading",
		"TRAIL_SPACE":   "trailing   ",
		"BOTH_SPACE":    "   both   ",
		"ALL_SPACE":     "    ",
		"BACKSLASH":     `a\b\c\\d`,
		"EQUALS":        "left=right",
		"HASH":          "value # not a comment",
		"DOLLAR":        "$HOME and ${PATH}",
		"PEM_BLOCK":     "-----BEGIN-----\nline1\nline2\n-----END-----",
		"JSON_PAYLOAD":  `{"k":"v","nested":{"x":1}}`,
		"UNICODE":       "naïve café 🚀",
		"MIXED_CHAOS":   "  \n\"quoted\"\nline2\\back\nend  ",
	}
	encoded := Format(cases)
	got := Parse(encoded)
	if !reflect.DeepEqual(got, cases) {
		for k, want := range cases {
			if g := got[k]; g != want {
				t.Errorf("round-trip lost %q:\n  want %q\n  got  %q", k, want, g)
			}
		}
		for k := range got {
			if _, ok := cases[k]; !ok {
				t.Errorf("round-trip produced extra key %q", k)
			}
		}
	}
}

// Tolerance check: the parser must still understand legacy hand-written
// dotenv shapes (unquoted KEY=value, leading-whitespace lines, comments,
// single-quoted, trailing-whitespace) so users who hand-edited their
// `.env` before the round-trip fix aren't punished. The values returned
// here are the legacy parser's interpretation, intentionally lossy.
func TestParse_LegacyTolerance(t *testing.T) {
	body := `# comment line
   # indented comment
PLAIN=simple
TRAILING_WS=with-trailing
SINGLE='single-quoted'
DOUBLE="double-quoted"
EMPTY=
JUST_KEY
SPACED_KEY = padded
`
	got := Parse([]byte(body))
	want := map[string]string{
		"PLAIN":       "simple",
		"TRAILING_WS": "with-trailing",
		"SINGLE":      "single-quoted",
		"DOUBLE":      "double-quoted",
		"EMPTY":       "",
		"SPACED_KEY":  "padded",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy parse:\n  got %#v\n want %#v", got, want)
	}
}

// Format is deterministic: same map in, byte-identical output. The
// apps_diff path depends on this to fingerprint .env content.
func TestFormat_Deterministic(t *testing.T) {
	in := map[string]string{"Z": "1", "A": "2", "M": "3"}
	first := Format(in)
	second := Format(in)
	if string(first) != string(second) {
		t.Errorf("Format not deterministic:\n  first  %q\n  second %q", first, second)
	}
	// Sorted: A then M then Z.
	lines := strings.Split(strings.TrimRight(string(first), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "A=") || !strings.HasPrefix(lines[1], "M=") || !strings.HasPrefix(lines[2], "Z=") {
		t.Errorf("Format not sorted: %v", lines)
	}
}

// Multi-line double-quoted values must span newlines. This is the
// specific shape `runos apps pull` writes for a PEM block or any value
// containing `\n`. The legacy line-splitting parser would truncate at
// the first newline.
func TestParse_DoubleQuotedSpansLines(t *testing.T) {
	body := "PEM=\"-----BEGIN-----\\nline1\\nline2\\n-----END-----\"\nNEXT=plain\n"
	got := Parse([]byte(body))
	if got["PEM"] != "-----BEGIN-----\nline1\nline2\n-----END-----" {
		t.Errorf("PEM round-trip:\n  got %q", got["PEM"])
	}
	if got["NEXT"] != "plain" {
		t.Errorf("NEXT lost: %q", got["NEXT"])
	}
}

// Regression for issue 87: env values with stray C0 control bytes (or
// invalid UTF-8) silently passed through the CLI + conductor intake and
// blew up mid-orchestration at the kubectl apply step. Validate now
// refuses these pre-network; \n / \r / \t are still allowed for
// multi-line content (PEM blocks etc.).
func TestValidate(t *testing.T) {
	cases := []struct {
		name      string
		in        map[string]string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "plain ASCII passes",
			in:   map[string]string{"K": "value"},
		},
		{
			name: "newlines tab CR allowed (PEM-style)",
			in: map[string]string{
				"PEM": "-----BEGIN-----\nline1\nline2\n-----END-----",
				"TAB": "left\tright",
				"CRLF": "a\r\nb",
			},
		},
		{
			name:      "C0 control byte refused (the issue 87 repro)",
			in:        map[string]string{"BAD": "\x01\x02\x03\x04"},
			wantErr:   true,
			errSubstr: "control byte",
		},
		{
			name:      "DEL (0x7f) refused",
			in:        map[string]string{"BAD": "before\x7fafter"},
			wantErr:   true,
			errSubstr: "control byte",
		},
		{
			name:      "invalid UTF-8 refused",
			in:        map[string]string{"BAD": "\xff\xfe\xfd"},
			wantErr:   true,
			errSubstr: "UTF-8",
		},
		{
			name: "unicode passes",
			in:   map[string]string{"GREETING": "naïve café 🚀"},
		},
		{
			name:      "error names the offending key",
			in:        map[string]string{"GOOD": "ok", "BAD_KEY": "\x05nope"},
			wantErr:   true,
			errSubstr: "BAD_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSubstr)
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q missing substring %q", err, tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Empty map round-trips to empty bytes (not `\n`).
func TestFormat_EmptyMap(t *testing.T) {
	if got := Format(nil); len(got) != 0 {
		t.Errorf("Format(nil) = %q, want empty", got)
	}
	if got := Format(map[string]string{}); len(got) != 0 {
		t.Errorf("Format(empty map) = %q, want empty", got)
	}
}
