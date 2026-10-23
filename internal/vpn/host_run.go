package vpn

import (
	"errors"
	"os"
	"time"
)

// shutdownBudget is the whole time from the first stop signal to the end of the stop: the wait for a
// start that is still running AND the stop. One shared deadline, so the two cannot add up past
// launchd's 20 s kill timeout, and a hung external command cannot hold the daemon past it. A
// variable for tests.
var shutdownBudget = 15 * time.Second

// errStoppedWhileStarting is the exit reason when a stop signal arrives and start does not finish
// inside the budget. Whatever start had written to the machine is swept by the next daemon.
var errStoppedWhileStarting = errors.New("stopped by signal before the VPN daemon finished starting")

// errStopDidNotFinish is the exit reason when the stop outlasts the budget or is interrupted by a
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

// logExit writes a forced exit's reason to the daemon log and returns it. The returned error goes to
// stderr, which the launchd plist sends to /dev/null, so without this line `runos vpn logs` shows
// nothing about why the daemon left. A clean stop (nil) writes nothing here.
func logExit(err error) error {
	if err != nil {
		logEvent("exiting: %s", err)
	}
	return err
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
		// One deadline from the first signal covers the wait for start AND the stop.
		deadline := time.Now().Add(shutdownBudget)
		select {
		case result = <-started:
		case <-time.After(time.Until(deadline)):
			return logExit(errStoppedWhileStarting)
		}
		if result.err != nil {
			return result.err
		}
		return logExit(stopWithin(result.stop, time.Until(deadline), sig))
	}
	if result.err != nil {
		return result.err
	}
	<-sig
	return logExit(stopWithin(result.stop, shutdownBudget, sig))
}
