package vpn

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

/*
The daemon's own log file, bounded by the daemon.

WHY THE DAEMON OWNS A FILE. Reading the three service definitions (FCR349) showed that only launchd
named a log destination. The systemd unit and the Windows service named none: on Linux the output
went to the journal, which an ordinary user cannot read, and on Windows it went nowhere. And on
macOS the file is owned by launchd, so the daemon cannot rotate it.

A file the daemon opens itself is readable by the person who needs it without root, is the same on
every platform, and can keep itself small. WHO that is depends on the machine: on a shared host the
log (account, device and cluster ids, other systems' error text) is for the control-socket group
only (0640 root:<group>); a single-user machine with no group configured gets 0644. Output still goes
to stderr as well, so the journal and the launchd file keep what they had. When stderr IS a file
(launchd's StandardErrorPath) it carries the same lines, so it gets the same access rule.
*/

// maxDaemonLogBytes caps the live file; the previous generation is kept beside it, so the daemon
// holds at most twice this.
const maxDaemonLogBytes = 512 * 1024

type boundedLog struct {
	mu   sync.Mutex
	path string
	max  int64
	// group may read the file besides root; empty means a single-user machine (see applyLogAccess).
	group string
	f     *os.File
	size  int64
	// openFn opens the log file; a field so a test can make the reopen after a rotation fail.
	openFn func(path string) (*os.File, error)
}

func openBoundedLog(path string, max int64, group string) (*boundedLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	b := &boundedLog{path: path, max: max, group: group, openFn: openLogFile}
	if err := b.open(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *boundedLog) open() error {
	f, err := b.openFn(b.path)
	if err != nil {
		return err
	}
	// Who may read it is decided on every open, so a rotation never leaves a new file at the
	// umask's mercy and a file from an earlier build is brought into line.
	if err := applyLogAccess(f, b.path, b.group); err != nil {
		f.Close()
		return err
	}
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
	if b.f == nil {
		// A rotation could not reopen the file. Try again WITHOUT rotating: rotating again would
		// delete the <path>.1 the failed rotation just made.
		if err := b.open(); err != nil {
			return 0, err
		}
	} else if b.size > 0 && b.size+int64(len(p)) > b.max {
		b.rotate()
		if b.f == nil {
			return 0, errLogClosed
		}
	}
	n, err := b.f.Write(p)
	b.size += int64(n)
	return n, err
}

var errLogClosed = errors.New("the daemon log could not be reopened after rotating it")

// rotate keeps the old generation as <path>.1 and starts a new file. The handle is closed first
// because Windows will not rename an open file.
func (b *boundedLog) rotate() {
	_ = b.f.Close()
	_ = os.Remove(b.path + ".1")
	if err := os.Rename(b.path, b.path+".1"); err != nil {
		// Could not move it (a reader holds it open on Windows): truncate instead, so the cap
		// still holds. Only a regular file: Truncate follows a symlink.
		if checkLogPath(b.path) == nil {
			_ = os.Truncate(b.path, 0)
		}
	}
	b.f, b.size = nil, 0
	// On failure b.f stays nil; Write reports it and retries the open without rotating.
	_ = b.open()
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

// SetupDaemonLog points the standard logger at the bounded file (and stderr). The file is readable
// by the control-socket group (the people who may already control the VPN), or by everybody when no
// group is configured. It returns a func that closes the file. If the file cannot be opened the daemon carries on with stderr alone and
// says so there: a daemon that will not start because it cannot log has made things worse.
func SetupDaemonLog(path, socketGroup string, groupExplicit bool) (closeLog func()) {
	group := effectiveSocketGroup(socketGroup, groupExplicit)
	b, err := openBoundedLog(path, maxDaemonLogBytes, group)
	if err != nil {
		log.Printf("vpn: step=daemon-start status=warn cannot open the daemon log %s: %s (logging to stderr only)",
			path, redactText(err.Error()))
		return func() {}
	}
	narrowIfRegular(os.Stderr, group)
	log.SetOutput(teeWriter{file: b, stderr: os.Stderr})
	return func() { _ = b.Close() }
}
