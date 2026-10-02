//go:build !windows

package vpn

import (
	"fmt"
	"os"
	"syscall"
)

/*
Opening the daemon log safely, and deciding who may read it.

THE DAEMON IS ROOT AND /var/log CAN BE GROUP WRITABLE. A symlink planted at the log path would make
root append to (and chmod) whatever it points at; a FIFO would block the daemon's start forever.
So the path is checked with Lstat and opened with O_NOFOLLOW, and the open file is checked again
(the path can change between the two). A file owned by somebody else is refused too: it is not the
one this daemon created, and its owner could keep a descriptor to it.
*/

// checkLogPath refuses a path that exists and is not a regular file (a symlink counts).
func checkLogPath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (%s): refusing to use it as the daemon log", path, info.Mode().Type())
	}
	return nil
}

func openLogFile(path string) (*os.File, error) {
	if err := checkLogPath(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file: refusing to use it as the daemon log", path)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		f.Close()
		return nil, fmt.Errorf("%s is owned by another user (uid %d): refusing to use it as the daemon log", path, st.Uid)
	}
	return f, nil
}

// applyLogAccess sets who may read the open log. With a socket group: 0640, group-owned by it, so
// only root and the people who can already control the VPN read it. A group that does not exist
// leaves the file root-only, as the socket is. With no group configured (a single-user machine)
// 0644. The mode is set before the group so a failure between the two can only be too strict.
func applyLogAccess(f *os.File, _ string, group string) error {
	if group == "" {
		return f.Chmod(0o644)
	}
	if err := f.Chmod(0o640); err != nil {
		return err
	}
	gid, ok := groupGID(group)
	if !ok {
		return nil
	}
	if err := f.Chown(-1, gid); err != nil {
		return fmt.Errorf("give the daemon log to group %q: %w", group, err)
	}
	return nil
}

// narrowIfRegular applies the log access rule to f when it is a regular file: under launchd stderr
// is a file the daemon did not open, carrying the same lines as the log. A pipe or a journal is
// left alone, and so is a failure: this is a hardening step, not a reason to stop the daemon.
func narrowIfRegular(f *os.File, group string) {
	if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
		_ = applyLogAccess(f, "", group)
	}
}
