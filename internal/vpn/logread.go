package vpn

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// LogResult is what reading the daemon log produced. Path is empty when no log file exists.
type LogResult struct {
	Path    string   `json:"path,omitempty"`
	Lines   []string `json:"lines"`
	Total   int      `json:"totalLines"`
	Skipped int      `json:"noiseLinesSkipped"`
	Err     error    `json:"-"`
}

/*
Problem says in plain words what went wrong reading the log, or "" when nothing did.

A permission error gets its own wording because it is the likely one now that the log is readable
only by the control-socket group, and "no log" or a bare "permission denied" would send the reader
looking for a file that is there.
*/
func (r LogResult) Problem() string {
	if r.Err == nil {
		return ""
	}
	if errors.Is(r.Err, fs.ErrPermission) {
		who := "root and the control-socket group"
		if g := socketGroupName(r.Path); g != "" {
			who = fmt.Sprintf("root and the group %q", g)
		}
		return fmt.Sprintf("the daemon log %s exists but this user cannot read it: only %s may. "+
			"Run this command with sudo, or add your user to that group and sign in again.", r.Path, who)
	}
	return r.Err.Error()
}

// DefaultLogPaths lists where to look for the daemon log, newest location first. An older daemon,
// which has not been restarted since an update, still writes to the legacy one.
func DefaultLogPaths() []string { return append([]string{DaemonLogPath}, legacyLogPaths()...) }

/*
Lines the daemon did not write and nobody can act on.

`MallocStackLogging` is emitted by the macOS allocator into the process's stderr whenever a tool
inherits the environment variable, and a Go binary spawning helpers produces two per invocation.
It is pure noise here: it says nothing about the tunnel and it is what buried the real content.
*/
func isLogNoise(line string) bool {
	return strings.Contains(line, "MallocStackLogging") || strings.TrimSpace(line) == ""
}

const (
	// maxLogLines bounds what ReadLog returns, also for tail 0 ("all"): a legacy launchd file is not
	// bounded by anyone and can be gigabytes.
	maxLogLines = 10000
	// maxLogLineBytes is the longest line ReadLog keeps. The daemon writes short lines; a longer one
	// is replaced by a marker rather than cut, because a cut can leave half a credential in view.
	maxLogLineBytes = 16 * 1024
)

/*
ReadLog reads the log at the first path that has one, drops the noise, and returns the last tail
lines (the last maxLogLines when tail is 0), redacted. It needs no privilege beyond the file being
readable, which the daemon makes it for the control-socket group. A file that exists but cannot be read is reported in Err.

THE ROTATED GENERATION IS PART OF THE LOG. The daemon writes a step once, on change, and rotates
its file by size (logfile.go). A failure written just before a rotation is then only in <path>.1,
and a still broken machine would read as "none recorded". So <path>.1 is read first and the live
file after it, oldest first.

ONLY THE LAST LINES ARE KEPT, and only those are redacted: the file can be far larger than the
answer, and the lines that are dropped never need the work.
*/
func ReadLog(paths []string, tail int) LogResult {
	keep := tail
	if keep <= 0 || keep > maxLogLines {
		keep = maxLogLines
	}
	var failed *LogResult
	for _, path := range paths {
		res := LogResult{}
		ring := newLineRing(keep)
		for _, name := range []string{path + ".1", path} {
			exists, err := scanLog(name, ring, &res)
			if exists {
				res.Path = path
			}
			if err != nil && res.Err == nil {
				res.Err = err
			}
		}
		if res.Path == "" {
			continue
		}
		res.Lines = redactLines(ring.lines())
		if res.Err != nil && len(res.Lines) == 0 {
			// Unreadable here; a later location may still have the log.
			if failed == nil {
				failed = &res
			}
			continue
		}
		return res
	}
	if failed != nil {
		return *failed
	}
	return LogResult{}
}

// scanLog feeds one file's lines to the ring and the counters in res. exists is false only when the
// file is absent; an unreadable file exists and carries its error.
func scanLog(name string, ring *lineRing, res *LogResult) (exists bool, err error) {
	file, err := os.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := readLogLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return true, nil
			}
			return true, fmt.Errorf("read %s: %w", name, err)
		}
		if isLogNoise(line) {
			res.Skipped++
			continue
		}
		res.Total++
		ring.push(line)
	}
}

// readLogLine returns one line without its newline, whatever its length. A line over
// maxLogLineBytes is consumed to its end and replaced by a marker, so one huge line cannot end the
// scan (the old bufio.Scanner stopped there and silently dropped the rest of the file).
func readLogLine(r *bufio.Reader) (string, error) {
	var buf []byte
	total := 0
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if err == io.EOF && total > 0 {
				break
			}
			return "", err
		}
		total += len(chunk)
		if total <= maxLogLineBytes {
			buf = append(buf, chunk...)
		}
		if !isPrefix {
			break
		}
	}
	if total > maxLogLineBytes {
		return fmt.Sprintf("<line of %d bytes omitted>", total), nil
	}
	return string(buf), nil
}

// lineRing keeps the newest n lines.
type lineRing struct {
	buf  []string
	next int
	n    int
}

func newLineRing(n int) *lineRing { return &lineRing{n: n} }

func (r *lineRing) push(line string) {
	if len(r.buf) < r.n {
		r.buf = append(r.buf, line)
		return
	}
	r.buf[r.next] = line
	r.next = (r.next + 1) % r.n
}

// lines returns the kept lines, oldest first.
func (r *lineRing) lines() []string {
	if len(r.buf) < r.n || r.next == 0 {
		return r.buf
	}
	return append(append([]string(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}
