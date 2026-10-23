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
var errStoppedWhileStarting = errors.New("stopped by signal before the VPN daemon finished starting")

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
		result.stop()
		return nil
	}
	if result.err != nil {
		return result.err
	}
	<-sig
	result.stop()
	return nil
}
