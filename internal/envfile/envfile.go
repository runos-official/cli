// Package envfile reads and writes dotenv-style env files losslessly.
//
// The naive `KEY=value` line-per-pair format the CLI previously used
// silently mangled values that contained newlines, leading/trailing
// whitespace, or quote characters: writers emitted them as-is and
// readers stripped them on the way back in. That broke the canonical
// `runos apps pull` -> edit -> `runos apps sync` round-trip when
// upstream env vars carried any of those characters (multi-line PEM
// keys, JSON blobs, quoted phrases). This package replaces that
// codepath with a quoted format that round-trips byte-for-byte.
//
// Wire format (writer):
//
//	KEY="value with escapes \\ \" \n \r \t"
//
// Always double-quoted so we can carry any byte sequence; the writer
// escapes `\\`, `"`, newline, carriage return, and tab. Comments are
// not emitted (we don't carry comments through the round-trip; the
// reader still tolerates them on input).
//
// Wire format (reader): the parser is permissive and accepts every
// shape the legacy parser did, plus the new escaped form:
//
//   - Blank lines and `# comment` lines (must start with `#` after
//     optional leading whitespace).
//   - Unquoted: `KEY=value` (trailing whitespace stripped). No escapes.
//   - Double-quoted: `KEY="value"` (escapes `\\`, `\"`, `\n`, `\r`,
//     `\t`; everything else after a backslash is taken literally).
//     Spans multiple lines.
//   - Single-quoted: `KEY='value'` (no escapes; can't contain `'`).
//     Spans multiple lines.
//
// Issue 73: `runos apps pull` -> edit -> `runos apps sync` no longer
// drops newlines, leading whitespace, or quote chars in env values.
package envfile

import (
	"fmt"
	"sort"
	"strings"
)

// Parse reads a dotenv-style byte payload and returns the key-value map.
// The parser is permissive: it returns the keys it understands and
// silently skips malformed lines (so a hand-edited file with a stray
// non-`KEY=value` line doesn't take down the whole round-trip).
func Parse(data []byte) map[string]string {
	out := map[string]string{}
	s := string(data)
	i := 0
	n := len(s)
	for i < n {
		// Skip leading whitespace within a line, but stop at newline so
		// blank lines remain blank.
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= n {
			break
		}
		// Blank line.
		if s[i] == '\n' {
			i++
			continue
		}
		// Comment line.
		if s[i] == '#' {
			for i < n && s[i] != '\n' {
				i++
			}
			if i < n {
				i++
			}
			continue
		}
		// Read key up to `=` or end-of-line.
		keyStart := i
		for i < n && s[i] != '=' && s[i] != '\n' {
			i++
		}
		key := strings.TrimSpace(s[keyStart:i])
		if i >= n || s[i] == '\n' {
			// Malformed line (no `=`); skip it.
			if i < n {
				i++
			}
			continue
		}
		// Consume `=` and any whitespace between `=` and the value.
		i++
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		// Decode the value.
		value, advanced := decodeValue(s, i)
		i = advanced
		// Consume rest of line.
		for i < n && s[i] != '\n' {
			i++
		}
		if i < n {
			i++
		}
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// decodeValue reads a value starting at s[i] and returns (value, new
// index after the value). Handles double-quoted, single-quoted, and
// unquoted shapes. For unquoted, reads to the end of the line and
// strips trailing whitespace.
func decodeValue(s string, i int) (string, int) {
	n := len(s)
	if i >= n {
		return "", i
	}
	switch s[i] {
	case '"':
		i++
		var b strings.Builder
		for i < n {
			c := s[i]
			if c == '"' {
				return b.String(), i + 1
			}
			if c == '\\' && i+1 < n {
				switch s[i+1] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"':
					b.WriteByte('"')
				case '\\':
					b.WriteByte('\\')
				default:
					b.WriteByte(s[i+1])
				}
				i += 2
				continue
			}
			b.WriteByte(c)
			i++
		}
		// Unterminated double-quote: take everything we got.
		return b.String(), i
	case '\'':
		i++
		start := i
		for i < n && s[i] != '\'' {
			i++
		}
		val := s[start:i]
		if i < n {
			i++
		}
		return val, i
	default:
		start := i
		for i < n && s[i] != '\n' {
			i++
		}
		return strings.TrimRight(s[start:i], " \t\r"), i
	}
}

// Format serialises envVars to a byte payload that Parse round-trips
// byte-for-byte. Keys are emitted in sorted order so two invocations
// with the same map produce byte-identical output (useful for
// content-fingerprint comparisons in apps_diff / apps_sync). Every
// value is double-quoted with the standard four escapes so a value
// containing any character byte sequence is faithfully preserved.
func Format(envVars map[string]string) []byte {
	keys := make([]string, 0, len(envVars))
	for k := range envVars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, encodeValue(envVars[k]))
	}
	return []byte(b.String())
}

func encodeValue(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
