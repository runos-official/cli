package vpn

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

/*
What the daemon writes to its log, and what it deliberately does not.

WHY THIS EXISTS. `/var/log/runos-vpn.log` already received the daemon's stdout and stderr, because
the LaunchDaemon sets StandardOutPath and StandardErrorPath. But the daemon wrote almost nothing to
them. Measured on a real machine 2026-09-01, after a boot-time DNS failure had left the VPN down
for fourteen minutes: the file held 582 KB of macOS `MallocStackLogging` noise and ONE useful line,
the socket-permission line from startup. The failure that broke the tunnel produced no entry at all.

So asking a user to send their logs returned half a megabyte that could not answer the question.

WHAT IS LOGGED. Transitions and failures, never a heartbeat. A poll that succeeds and changes
nothing writes nothing, because a line per poll is 2880 lines a day that bury the one line that
matters, and the log file is owned by launchd so the daemon cannot rotate it. The events worth a
line are the ones that change what the tunnel is doing, plus every failure, plus the RECOVERY after
a failure, because "it started working again at 12:31" is exactly what a support conversation needs
and is invisible if only errors are recorded.

FCR349 ADDED THE CONNECT PATH. A new user never reached "tunnel up", so the log was empty exactly when
it was needed. Every step a connect takes now writes one `step=<name> status=ok|failed|warn` line
(steps.go), still on change only: stepOutcome writes a step's first success, each NEW failure reason,
and the recovery, and the handshake watch writes the first handshake and any change after it. The
CLI half (sign-in, enrolment, the session mint) cannot be seen from here, so `runos vpn up` keeps its
own trace (trace.go). `runos vpn diagnose` reads both.

THE FILE IS BOUNDED BY THE DAEMON NOW. It writes its own log (logfile.go) and rotates it, which is
what launchd's file never allowed. wireguard-go's own error lines go through the same file, rate
limited, because one dead endpoint produces a line every five seconds.

WHAT IS NEVER LOGGED. Session tokens, private keys, pre-shared keys, and the WireGuard
configuration. Account, device and cluster ids ARE logged: they appear in `vpn status` already, they
are what identifies a report, and they are not credentials. `redactURL` strips any query string
before a URL is written, because a signed URL carries its credential there.
*/

// logState remembers what was last reported, so a repeated condition does not repeat its line and
// a recovery can be recognised.
type logState struct {
	mu           sync.Mutex
	lastPollFail string
	failedPolls  int
	firstFailAt  time.Time
	// Per-step outcome memory for stepOutcome: the last failure message, and whether a success
	// was already written since the tunnel came up.
	stepFailed map[string]string
	stepSeen   map[string]bool
}

var daemonLog logState

// redactURL strips a query string, which is where a signed URL carries its credential.
func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i] + "?<redacted>"
	}
	return raw
}

// logEvent writes one line for something that changed. Use for transitions, never for a heartbeat.
func logEvent(format string, args ...any) {
	log.Printf("vpn: "+format, args...)
}

/*
Record the outcome of a poll, writing a line only when the situation CHANGES.

The first failure is logged with the error. Repeats are counted and stay silent, so a network that
is down for an hour produces one line rather than 120. The recovery is logged with how long it took
and how many attempts it cost, which is the line that answers "when did it come back".
*/
func logPollOutcome(err error) {
	daemonLog.mu.Lock()
	defer daemonLog.mu.Unlock()

	if err != nil {
		msg := redactURL(err.Error())
		if daemonLog.lastPollFail == msg {
			daemonLog.failedPolls++
			return
		}
		daemonLog.lastPollFail = msg
		daemonLog.failedPolls = 1
		daemonLog.firstFailAt = time.Now()
		log.Print("vpn: " + stepBody("poll", statusFailed,
			fmt.Sprintf("poll FAILED, will keep retrying every %s: %s", PollInterval, msg)))
		return
	}

	if daemonLog.lastPollFail != "" {
		log.Print("vpn: " + stepBody("poll", statusOK, fmt.Sprintf(
			"poll recovered after %d failed attempt(s) over %s (last error: %s)",
			daemonLog.failedPolls,
			time.Since(daemonLog.firstFailAt).Round(time.Second),
			daemonLog.lastPollFail,
		)))
		daemonLog.lastPollFail = ""
		daemonLog.failedPolls = 0
	}
}

// resetPollLog forgets the failure history, so a fresh tunnel does not report a recovery from a
// failure that belonged to the previous one, and so every step of a new connect is written once.
func resetPollLog() {
	daemonLog.mu.Lock()
	defer daemonLog.mu.Unlock()
	daemonLog.lastPollFail = ""
	daemonLog.failedPolls = 0
	daemonLog.stepFailed = nil
	daemonLog.stepSeen = nil
}

// logDaemonStart writes the one line that says which build is running and where, so a log read
// later can be matched to a version and a platform. One line per daemon start.
func logDaemonStart(version string, verbose bool) {
	logEvent("step=daemon-start status=ok runos %s on %s/%s (verbose=%t)", version, runtime.GOOS, runtime.GOARCH, verbose)
}

// LogDaemonFailure records why the daemon process could not start. The Windows service manager
// discards stderr, and a root process that exits at once leaves no other trace of the reason.
func LogDaemonFailure(step string, err error) {
	if err == nil {
		return
	}
	log.Print("vpn: " + stepBody(step, statusFailed, err.Error()))
}

// KeyFingerprint is the first characters of a public key: enough to tell two keys apart in a log
// and to compare with `vpn devices`, and nowhere near the key itself.
func KeyFingerprint(publicKey string) string {
	if len(publicKey) > 8 {
		return publicKey[:8]
	}
	return publicKey
}

// interfaceError names the usual cause when the tun device cannot be created by a process that is
// not root, because the raw errno ("operation not permitted") names nothing a person can act on.
func interfaceError(err error) error {
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("%w (creating a network interface needs root: the daemon must run under the service manager, not as a normal user)", err)
	}
	return err
}
