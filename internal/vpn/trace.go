package vpn

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

/*
The CLI's record of the last `runos vpn up`.

Sign-in, device registration and the session mint run in the PERSON's process, not in the daemon, so
the daemon log cannot know they happened or why they failed. The CLI writes its own short trace, in
the same step format, to a file the person owns (mode 0600, under their config directory).

It holds ONE attempt: each `up` replaces it. A trace that accumulated would show last week's fixed
problem beside today's, and a person reading it could not tell which was current. Every `up` writes
roughly a dozen lines, so it is bounded by construction.

All methods are safe on a nil *Trace, which is what a command gets when the home directory is not
writable. A VPN that will not connect must not also refuse to say why.
*/
type Trace struct{ path string }

// StartTrace replaces the trace at path with a header for a new attempt. Nil when it cannot write.
func StartTrace(path, cliVersion string) *Trace {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	header := stamp() + " vpn-cli: up started, runos " + cliVersion + " on " + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if err := os.WriteFile(path, []byte(header), 0o600); err != nil {
		return nil
	}
	return &Trace{path: path}
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339) }

func (t *Trace) write(step, status, detail string) {
	if t == nil {
		return
	}
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(stamp() + " vpn-cli: " + stepBody(step, status, detail) + "\n")
}

// OK records a step that worked.
func (t *Trace) OK(step, detail string) { t.write(step, statusOK, detail) }

// Warn records a step that did not fail but is worth knowing (a fresh sign-in was requested).
func (t *Trace) Warn(step, detail string) { t.write(step, statusWarn, detail) }

// Fail records a step that failed, with the error text.
func (t *Trace) Fail(step string, err error) {
	if err == nil {
		return
	}
	t.write(step, statusFailed, err.Error())
}

// ReadTrace returns the lines of the last attempt, redacted. Nil when there is none.
func ReadTrace(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, redactText(line))
		}
	}
	return lines
}
