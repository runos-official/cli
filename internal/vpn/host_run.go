package vpn

import (
	"errors"
	"os"
	"time"
)

// startStopGrace is how long a stop signal waits for a start that is still running. A start that
// finishes inside it is stopped cleanly. One that does not is abandoned and the process exits, so a
// hung external command cannot hold the daemon past the host's own kill timeout (launchd: 20 s).
// A variable for tests.
var startStopGrace = 10 * time.Second

// errStoppedWhileStarting is the exit reason when a stop signal arrives and start does not finish
// inside startStopGrace. Whatever start had written to the machine is swept by the next daemon.
// stopTimeout is how long the stop may run after a stop signal. Under launchd's 20 s kill timeout, so
// the process exits with a reason instead of ending in SIGKILL. A variable for tests.
var stopTimeout = 15 * time.Second

var errStoppedWhileStarting = errors.New("stopped by signal before the VPN daemon finished starting")

// errStopDidNotFinish is the exit reason when the stop outlasts stopTimeout or is interrupted by a
// second signal. The next start sweeps whatever the stop left.
var errStopDidNotFinish = errors.New("the VPN daemon did not finish stopping in time")

/*
stopWithin runs stop on its own goroutine and waits for it, up to limit. A second stop signal (when
sig is not nil) ends the wait at once. The stop takes the daemon lock, which a resume or a poll can
hold through external commands, so it has no bound of its own.
*/
func stopWithin(stop func(), limit time.Duration, sig <-chan os.Signal) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errStopDidNotFinish
	case <-sig:
		return errStopDidNotFinish
	}
}

type startResult struct {
	stop func()
	err  error
}

/*
runHost runs start on its own goroutine and waits for either a stop signal or the end of start.

Start must not run on the goroutine that reads the signals: it takes the instance lock (up to 15 s)
and resumes the session, and both can block on things outside this process.
*/
func runHost(start func() (stop func(), err error), sig <-chan os.Signal) error {
	started := make(chan startResult, 1)
	go func() {
		stop, err := start()
		started <- startResult{stop, err}
	}()

	var result startResult
	select {
	case result = <-started:
	case <-sig:
		select {
		case result = <-started:
		case <-time.After(startStopGrace):
			return errStoppedWhileStarting
		}
		if result.err != nil {
			return result.err
		}
		return stopWithin(result.stop, stopTimeout, sig)
	}
	if result.err != nil {
		return result.err
	}
	<-sig
	return stopWithin(result.stop, stopTimeout, sig)
}
