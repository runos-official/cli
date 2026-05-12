package cmd

import "testing"

// I4-H regression: pre-fix, `runos apps diff` defaulted to plain
// secret values regardless of where stdout pointed, so CI pipelines
// piping the output (or capture-to-file invocations) baked
// `DATABASE_URL=postgresql://...` straight into build logs. The
// auto-redact rule promotes redaction to the default when stdout
// is not a TTY (pipes, redirects, MCP shell-out) while preserving
// the interactive-terminal behaviour CLAUDE.md documents.
func TestShouldAutoRedact(t *testing.T) {
	cases := []struct {
		name           string
		explicitRedact bool
		explicitShow   bool
		stdoutIsTTY    bool
		want           bool
	}{
		// Explicit --redact-secrets wins regardless of TTY / show flag.
		{"explicit redact, TTY", true, false, true, true},
		{"explicit redact, pipe", true, false, false, true},
		{"explicit redact + show: redact wins", true, true, true, true},

		// Explicit --show-secrets wins regardless of TTY (when redact
		// not also set).
		{"explicit show, TTY", false, true, true, false},
		{"explicit show, pipe", false, true, false, false},

		// No explicit flag: auto-redact iff piped/redirected.
		{"no flags, TTY: plain", false, false, true, false},
		{"no flags, pipe: redact (I4-H)", false, false, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldAutoRedact(c.explicitRedact, c.explicitShow, c.stdoutIsTTY)
			if got != c.want {
				t.Errorf("shouldAutoRedact(redact=%v, show=%v, tty=%v) = %v, want %v",
					c.explicitRedact, c.explicitShow, c.stdoutIsTTY, got, c.want)
			}
		})
	}
}
