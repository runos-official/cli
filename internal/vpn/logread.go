package vpn

import (
	"bufio"
	"errors"
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

// ReadLog reads the first log file that exists, drops the noise, redacts what is left and returns
// the last tail lines (all of them when tail is 0). It needs no privilege beyond the file being
// readable, which the daemon makes it. A file that exists but cannot be read is reported in Err.
func ReadLog(paths []string, tail int) LogResult {
	var res LogResult
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				res.Path, res.Err = path, err
			}
			continue
		}
		defer file.Close()
		res = LogResult{Path: path}
		scanner := bufio.NewScanner(file)
		// A daemon line is short; this raises the cap only so one very long error cannot end the
		// scan early and silently truncate the log.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var kept []string
		for scanner.Scan() {
			line := scanner.Text()
			if isLogNoise(line) {
				res.Skipped++
				continue
			}
			kept = append(kept, redactText(line))
		}
		res.Err = scanner.Err()
		res.Total = len(kept)
		res.Lines = kept
		if tail > 0 && len(kept) > tail {
			res.Lines = kept[len(kept)-tail:]
		}
		return res
	}
	return res
}
