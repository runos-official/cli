package vpn

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

/*
The daemon's own log file, bounded by the daemon.

WHY THE DAEMON OWNS A FILE. Reading the three service definitions (FCR349) showed that only launchd
named a log destination. The systemd unit and the Windows service named none: on Linux the output
went to the journal, which an ordinary user cannot read, and on Windows it went nowhere. And on
macOS the file is owned by launchd, so the daemon cannot rotate it.

A file the daemon opens itself is readable by the person who needs it (mode 0644, no root), is the
same on every platform, and can keep itself small. Output still goes to stderr as well, so the
journal and the launchd file keep what they had.
*/

// maxDaemonLogBytes caps the live file; the previous generation is kept beside it, so the daemon
// holds at most twice this.
const maxDaemonLogBytes = 512 * 1024

type boundedLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func openBoundedLog(path string, max int64) (*boundedLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	b := &boundedLog{path: path, max: max}
	if err := b.open(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *boundedLog) open() error {
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// The umask can narrow the create mode, and a log nobody but root can read is the failure
	// this file exists to remove.
	_ = f.Chmod(0o644)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	b.f, b.size = f, info.Size()
	return nil
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size > 0 && b.size+int64(len(p)) > b.max {
		b.rotate()
	}
	n, err := b.f.Write(p)
	b.size += int64(n)
	return n, err
}

// rotate keeps the old generation as <path>.1 and starts a new file. The handle is closed first
// because Windows will not rename an open file.
func (b *boundedLog) rotate() {
	_ = b.f.Close()
	_ = os.Remove(b.path + ".1")
	if err := os.Rename(b.path, b.path+".1"); err != nil {
		// Could not move it (a reader holds it open on Windows): truncate instead, so the cap
		// still holds.
		_ = os.Truncate(b.path, 0)
	}
	if err := b.open(); err != nil {
		// Nothing sensible to do with a daemon that cannot log. The next write reports the closed file.
		b.f = nil
	}
}

func (b *boundedLog) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.f == nil {
		return nil
	}
	return b.f.Close()
}

// teeWriter writes to the file first and to stderr second, and ignores a stderr that fails. Under
// the Windows service manager there is no console, and io.MultiWriter would stop at that error and
// never reach the file.
type teeWriter struct {
	file   io.Writer
	stderr io.Writer
}

func (t teeWriter) Write(p []byte) (int, error) {
	n, err := t.file.Write(p)
	_, _ = t.stderr.Write(p)
	return n, err
}

// SetupDaemonLog points the standard logger at the bounded file (and stderr). It returns a func
// that closes the file. If the file cannot be opened the daemon carries on with stderr alone and
// says so there: a daemon that will not start because it cannot log has made things worse.
func SetupDaemonLog(path string) (closeLog func()) {
	b, err := openBoundedLog(path, maxDaemonLogBytes)
	if err != nil {
		log.Printf("vpn: step=daemon-start status=warn cannot open the daemon log %s: %s (logging to stderr only)",
			path, redactText(err.Error()))
		return func() {}
	}
	log.SetOutput(teeWriter{file: b, stderr: os.Stderr})
	return func() { _ = b.Close() }
}

// wireguardLogInterval is the least time between two identical engine log lines.
var wireguardLogInterval = time.Minute

/*
rateLimitedLogf wraps the engine's error logger so one persistent fault cannot flood the file.

wireguard-go retries a failing handshake every few seconds and logs each failure. A server that
cannot be reached would write thousands of identical lines a day, burying the step lines that matter
and filling a file the daemon is trying to keep small. The first occurrence of a message is written;
repeats inside the interval are counted, and the count is written with the next one.
*/
func rateLimitedLogf(next func(format string, args ...any)) func(format string, args ...any) {
	var mu sync.Mutex
	last := map[string]time.Time{}
	dropped := map[string]int{}
	return func(format string, args ...any) {
		mu.Lock()
		now := time.Now()
		if t, ok := last[format]; ok && now.Sub(t) < wireguardLogInterval {
			dropped[format]++
			mu.Unlock()
			return
		}
		last[format] = now
		n := dropped[format]
		dropped[format] = 0
		mu.Unlock()
		if n > 0 {
			next("(%d similar line(s) suppressed in the last %s)", n, wireguardLogInterval)
		}
		next(format, args...)
	}
}
