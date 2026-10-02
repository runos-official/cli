//go:build windows

package vpn

import (
	"fmt"
	"os"
)

// checkLogPath: Windows has no O_NOFOLLOW and the daemon's directory is locked down by
// SecureStateDir, so there is nothing to check here beyond the open itself.
func checkLogPath(string) error { return nil }

func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// applyLogAccess lets local Users read the log, the same audience the control socket is opened to
// (grantSocketAccess). The directory it sits in is closed to everyone but SYSTEM and
// Administrators, so without this grant nobody else could read it. The group argument has no
// meaning here.
func applyLogAccess(f *os.File, path string, _ string) error {
	if out, err := run("icacls", logReadArgs(path)...); err != nil {
		return fmt.Errorf("grant read access to the daemon log: %w: %s", err, out)
	}
	return nil
}

// narrowIfRegular: stderr is not a file under the Service Control Manager.
func narrowIfRegular(*os.File, string) {}
