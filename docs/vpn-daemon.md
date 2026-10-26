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

## Private DNS on macOS

The daemon writes `/etc/resolver/<zone>` and then checks that macOS lists the zone in `scutil --dns`.
macOS applies the file on its own schedule: under a second on one run, and not within two minutes on
another, with the file on disk and the resolver reachable through the tunnel (FCR 1730).

- A zone that macOS has not listed yet is **pending**, not a failure. `up` succeeds, `status` says
  "Connected. Cluster names are still being applied by macOS", and `DNSStatus.Pending` is true. The
  field is omitted when false, so a client that predates it reads the same JSON.
- A watch in the daemon (`dns_pending.go`, timing in `dns_schedule.go`) keeps looking, twice a second
  for ten seconds and then every five. It re-publishes the file (an atomic rewrite and a flush) after
  three seconds and every fifteen after, and gives up after three minutes with a plain failed state.
  The tunnel stays up. The next poll looks again, so a zone that macOS lists late still turns ready.
- The watch holds the daemon lock for one look at a time. A connect waits up to four seconds for the
  outcome, outside the lock, so `status` and `diagnose` are answered meanwhile.
- A second `up` for the same device while DNS is pending keeps the tunnel, the files and the clock.
- A real fault still fails the step: another resolver that owns the zone, a resolver file that
  RunOS did not write (never replaced), or a resolver directory that cannot be written.
- Resolver files are written to a temp file next to `/etc` and renamed into place, mode 0644, so no
  reader sees an empty or partial file. `make dnsprobe` (run it with sudo) times the old and the new
  method against `scutil --dns`.
- `runos vpn diagnose` reads the resolver files and `scutil --dns` without root and says whether the
  file is on disk but macOS does not list it.

Measured: the old write is observable as an empty file (a test reads it during rewrites). Theory, not
yet measured on a Mac: that this empty read is what macOS trips over (H1), and that re-publishing
makes macOS list a zone it skipped (H2). The probe settles H1.

## Time limits

| Limit | Value | Where |
|---|---|---|
| Wait for the old daemon to leave (instance lock) | 15 s | `InstanceLockWait` |
| Shutdown after the first stop signal: the wait for a start that is still running PLUS the stop, one shared deadline (a second signal ends the wait) | 15 s in total | `shutdownBudget` |
| Any external command (route, scutil, PowerShell, resolvectl) | 10 s | `commandTimeout` |
| Installer and restart commands (launchctl 30 s, systemctl 100 s) | 30 s / 100 s | `serviceCommandLimit` |
| A request waiting for the resume | 30 s | `startRequestWait` |
| One socket connection | 60 s | `serveConn` |
| The status question that `runos update` asks | 2 s | `daemonStatusTimeout` |

What is guaranteed after a stop signal:

- A stop makes a running resume or apply return at the next step. `Close` sets a flag before it takes the
  daemon lock, and the resume and the apply check it before the tunnel, the address, the WireGuard
  configuration, each route and the DNS step. A step that is already running (one external command, or
  the 10 s HTTP poll) finishes first.
- The host leaves within `shutdownBudget` (15 s) of the first stop signal, whatever mix of a running start
  and a slow stop uses it. It then exits with a reason. The budget is one deadline, not a grace plus a
  stop limit, so the two cannot add up past the 20 s launchd kill timeout.
  It is a bound on how long the host waits, not on how long the work takes: what the stop did not finish
  is swept by the next start.
- A daemon that was stopped does not resume. A resume that starts after `Close` returns at once.

The values are chosen to stay under the macOS kill timeout (20 s). The 10 s command limit alone does not
guarantee that: the commands of one resume can add up to more. That is why the stop has its own limit.

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
   just before the stop can call `Handle` on a daemon that is shutting down. It can no longer start a
   tunnel: the closed checks in `startTunnelLocked` and `beginPollingLocked` make an `up` after `Close`
   answer "the VPN daemon is stopping", and the engine is not created (a test shows 0 engine requests).
   Two things remain. `Handle` itself does not check the flag, so other operations still run against a
   closed daemon. And a refused `up` still saves the new session to the state file before it is refused.
   That is harmless: the next daemon resumes the saved session. Plan: make `Handle` answer "stopping"
   when the flag is set, before it saves anything.
7. **An empty resolver file from an older daemon is not swept.** Older builds truncated the file before
   they wrote it, so a kill between the two left an empty file with no marker, and the sweep does not
   remove it. The current build writes through a rename, so it no longer makes one. It replaces an empty
   file the next time it writes that zone, but a zone that is never written again keeps it.
8. **A managed file with no parsable nameserver is not swept.** The macOS sweep reads the files that
   `Resolvers` returns, and `Resolvers` skips a file whose nameserver line it cannot parse. A file that has
   the marker line and nothing else stays. The daemon never writes such a file. Plan: have `Teardown` list
   files by marker, not by nameserver.
9. **A timed-out command kills only the parent process.** `exec.CommandContext` kills the process it
   started, not its children, and `WaitDelay` stops the call from waiting on them. A grandchild of
   `powershell` or `sh` can outlive the timeout. The commands the daemon runs do not start long-lived
   children. Plan: run commands in their own process group and kill the group, if one ever does.
10. **A resume that started before `Close` finishes its current step.** The check is between steps, so a
    stop waits for the step that is running: up to 10 s for a command, or the HTTP poll. The host does not
    wait for this (see `shutdownBudget`); the next start sweeps what it left.
11. **The DNS flush can be skipped when a stop is killed late.** A stop that is killed after the last
    resolver file was removed but before the flush leaves a stale cache entry. The entries expire; the
    next start that removes a file flushes. Not planned.
12. **The exit code differs by platform.** A forced exit returns an error, so a unix daemon exits with
    code 1 (the service manager restarts it, which is wanted). The Windows service handler returns 0
    after a stop. Not planned; the reason is in the daemon log on both.

## Unknown: needs a real test

On macOS, `launchctl kickstart -k` is the restart. What signal it sends, and the delay before SIGKILL,
are taken from the launchd documentation and not measured here. On Windows nothing in `internal/vpn` has
run, because there is no Windows machine in the test setup. These need a real Windows test:

- `Get-DnsClientNrptRule` on a machine with no NRPT rules at all: does it return nothing, or an error?
  The teardown script assumes nothing and counts 0.
- The text PowerShell adds to merged output (CLIXML for errors, progress records when a script is run
  with `-NonInteractive`). The outcome parser reads the last line only, but this was not seen on a
  real run.
- A stop request while the service reports StartPending. Whether the service control manager refuses it
  is assumed, not tested (edge case 5).
