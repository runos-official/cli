# The VPN daemon: update, restart and known edge cases

This page is for people who change `internal/vpn` or the update path. It describes what happens to a
running tunnel when the program is updated, and it lists the edge cases that are known and not yet fixed.

## What an update does to a tunnel

- `runos update` writes the new program beside the old one and renames it into place. The running
  daemon keeps the old file open. It keeps the tunnel and keeps running the old build.
- The daemon picks up the new build only when the service restarts (`sudo runos vpn restart`, or the
  Desktop app's Restart VPN item). A restart cuts the tunnel for a few seconds.
- A stop (SIGTERM, or the service manager's stop) removes the split-DNS state and closes the interface.
  It does not end the session. The state file keeps the device key and the session token.
- A start sweeps first: it removes the split-DNS state that a daemon which did not exit cleanly left
  behind. It then resumes the stored session if the session has not lapsed. The connected clusters are
  not stored on the machine. Conductor holds them for the device, and the first poll returns them.
- Starting takes the instance lock (`daemon.lock` in the state directory), opens the control socket,
  and resumes in the background. A request that arrives during the resume waits up to 30 s, then gets a
  "still starting" answer.

## Time limits

| Limit | Value | Where |
|---|---|---|
| Wait for the old daemon to leave (instance lock) | 15 s | `InstanceLockWait` |
| Grace for a start that is still running after a stop signal | 10 s | `startStopGrace` |
| Any external command (route, scutil, PowerShell, resolvectl) | 10 s | `commandTimeout` |
| A request waiting for the resume | 30 s | `startRequestWait` |
| One socket connection | 60 s | `serveConn` |
| The status question that `runos update` asks | 2 s | `daemonStatusTimeout` |

The service manager's own kill timeout on macOS is 20 s by default. The values above are chosen to stay
under it. A daemon that is abandoned by the grace period leaves what it wrote; the next start sweeps it.

## Known edge cases (not fixed)

Each item has a plan. None is a security issue by itself: the state directory and the files involved are
root-owned.

1. **A daemon started with `--state-dir`.** The lock lives in the state directory, so two daemons with
   different state directories do not exclude each other. They would still share the control socket path
   and the split-DNS files. Plan: place the lock in a fixed system directory next to the control socket,
   and keep the state directory out of the lock path.
2. **A daemon from an older build.** It takes no lock. A new daemon that is started by hand while an old
   one runs would sweep the old one's DNS state. The service managers never do this, because they stop the
   old process first. Plan: none in code; `runos vpn restart` is the supported way, and the sweep logs a line.
3. **A dangling `daemon.lock` symlink created by root.** The lock file is opened with create, which
   follows a symlink, so the daemon would create the link's target (mode 0600) outside the state
   directory. Only root can plant it. Plan: open the lock file without following symlinks on unix.
4. **The lock path is a directory.** Opening fails and the daemon refuses to start, with the operating
   system's error. Plan: replace the error with a sentence that names the path and says to remove it.
5. **Windows: the service manager refuses a stop during StartPending.** The service handler does not accept
   a stop until it reports Running, and the instance-lock wait (up to 15 s) happens before that. Only a
   console run registers signals before start. Plan: run start on its own goroutine in the handler, as
   `runHost` does, and accept the stop control while starting.
6. **A request that is already past the listener can reach `Handle` after `Close`.** A connection accepted
   just before the stop can call `Handle` on a daemon that is shutting down (an `up` could start a tunnel
   that nothing then stops). This window existed before the update work. Plan: set a closed flag in
   `Close` and make `Handle` answer "stopping" when it is set.
