//go:build !windows

package vpn

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/user"
	"runtime"
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

/*
grantSocketAccess opens the control socket to the group that should be able to reach it.

THE HEAL RUNS HERE, on every daemon start, because this is the one place that runs as root and can
change the socket's owner without anybody typing a password. A machine installed before the
installer was fixed still has `wheel` in its service definition; one restart, or the next reboot,
puts the socket right. See usableSocketGroup for the narrow set of cases it will act on and the
larger set it refuses to touch.
*/
func grantSocketAccess(socketPath, socketGroup string, groupExplicit bool) error {
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}
	group := usableSocketGroup(socketGroup, runtime.GOOS, groupExplicit, groupGID, AdminGroup)
	if group != socketGroup {
		// LOUD, NOT SILENT. A root process changing who may control the VPN has to leave a record,
		// and this line is what an operator greps for when the group is not what their service
		// definition says.
		log.Printf("vpn: the configured control-socket group %q holds only root, so it cannot be "+
			"reached by the person who installed this. Using %q instead. Set --socket-group to "+
			"override.", socketGroup, group)
	}
	if group == "" {
		return nil
	}
	grp, err := user.LookupGroup(group)
	if err != nil {
		return nil
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return nil
	}
	_ = os.Chown(socketPath, -1, gid)
	/*
	 THE SOCKET'S STATE, RECORDED EVERY START.

	 The daemon wrote nothing of its own to its log: everything in it came from the WireGuard
	 engine. When two people could not reach the socket on 2026-08-31, nothing anywhere said what
	 group it had been given, so the answer had to be worked out from the outside. One line makes
	 this class of problem self-diagnosing, and it costs one write per daemon start.
	*/
	log.Printf("vpn: control socket %s is mode 0660, group %q (gid %d)", socketPath, group, gid)
	return nil
}
