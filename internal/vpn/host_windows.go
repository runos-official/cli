//go:build windows

package vpn

import (
	"fmt"
	"os"
	"os/signal"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// The Windows half of "how the daemon process is hosted": an elevated token is admin, the
// Service Control Manager is the host (Stop/Shutdown are the stop signal) when the process was
// started as a service, a terminal with Ctrl-C otherwise, and the socket file is opened to Users
// with an ACL grant.

// IsAdmin reports whether the current process is elevated.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// AdminHint is the phrase the install command prints when IsAdmin is false.
const AdminHint = "re-run 'runos vpn install' from an elevated (Run as administrator) prompt"

// RunDaemonHost starts the daemon through start and blocks until the host asks it to stop. Under
// the SCM the daemon is started INSIDE the service handler, after the dispatcher connected, so
// the SCM's start timeout is never spent on the first conductor poll.
func RunDaemonHost(start func() (stop func(), err error)) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("query service context: %w", err)
	}
	if !isService {
		stop, err := start()
		if err != nil {
			return err
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		stop()
		return nil
	}
	return svc.Run(serviceName, &scmHandler{start: start})
}

type scmHandler struct {
	start func() (stop func(), err error)
}

func (h *scmHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop, err := h.start()
	if err != nil {
		// A non-zero exit code makes the SCM record the failure and, with the recovery actions
		// Install sets, restart the service.
		return true, 1
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			status <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			stop()
			return false, 0
		}
	}
	return false, 0
}

// grantSocketAccess lets every local user connect: an AF_UNIX socket on Windows is reachable
// only with write access to its file object, and a file created by LocalSystem under ProgramData
// gives Users read only. The grant is to the well-known Users group SID, locale-independent.
func grantSocketAccess(socketPath, _ string) error {
	if out, err := run("icacls", socketPath, "/grant", "*S-1-5-32-545:(M)"); err != nil {
		return fmt.Errorf("grant socket access: %w: %s", err, out)
	}
	return nil
}
