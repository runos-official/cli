//go:build !windows

package vpn

import (
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
)

// The unix half of "how the daemon process is hosted": root is admin, SIGINT/SIGTERM is the stop
// signal (launchd and systemd both send SIGTERM), and the socket is opened to the installing
// user's group.

// IsAdmin reports whether the current process may install the service and create interfaces.
func IsAdmin() bool { return os.Geteuid() == 0 }

// AdminHint is the phrase the install command prints when IsAdmin is false.
const AdminHint = "re-run with 'sudo runos vpn install'"

// RunDaemonHost starts the daemon through start and blocks until the host asks it to stop, then
// runs the returned stop func. On unix the host is launchd or systemd (or a terminal) and the
// stop is a signal.
func RunDaemonHost(start func() (stop func(), err error)) error {
	stop, err := start()
	if err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	stop()
	return nil
}

func grantSocketAccess(socketPath, socketGroup string) error {
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	if socketGroup != "" {
		if grp, err := user.LookupGroup(socketGroup); err == nil {
			if gid, convErr := strconv.Atoi(grp.Gid); convErr == nil {
				_ = os.Chown(socketPath, -1, gid)
			}
		}
	}
	return nil
}
