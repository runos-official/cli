package vpn

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ErrDaemonAlreadyRunning means another daemon process holds the instance lock.
var ErrDaemonAlreadyRunning = errors.New("another RunOS VPN daemon is already running on this machine")

// InstanceLockWait is how long a starting daemon waits for the previous one to exit. launchd and
// systemd start the new process after the old one is gone, but a hand-started daemon, or a slow
// stop, can overlap by a few seconds. Longer than launchd's 20 s exit timeout would only delay the
// failure message.
const InstanceLockWait = 15 * time.Second

func instanceLockPath(stateDir string) string { return filepath.Join(stateDir, "daemon.lock") }

/*
AcquireInstanceLock makes this process the only daemon, and returns the func that gives it up.

TWO DAEMONS ON ONE MACHINE is a state nothing used to prevent. Serve deletes whatever socket file it
finds and binds its own, so a second daemon silently steals the control socket from the first. Both
then hold a utun, both write the same split-DNS files, and the first one to stop removes the files
the other still needs. The lock is an OS file lock, so it dies with the process: a crash or a
SIGKILL releases it, and there is no stale lock file to clean up.

The lock covers the daemon's whole life, so call it before anything touches the machine.
*/
func AcquireInstanceLock(stateDir string, wait time.Duration) (release func(), err error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(instanceLockPath(stateDir), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		locked, err := tryLockFile(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() {
				unlockFile(file)
				_ = file.Close()
			}, nil
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, ErrDaemonAlreadyRunning
		}
		time.Sleep(100 * time.Millisecond)
	}
}
