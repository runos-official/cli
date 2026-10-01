package vpn

import "log"

// stepOutcome records one connect step in the daemon log, and is written to be called freely.
//
// The poll loop re-asserts routes and DNS on every tick, so a step is reported only when its
// outcome CHANGES: the first success since the tunnel came up, a success whose detail differs from
// the last one written (a new document revision, a different peer count), a failure whose message
// is new, and the success that follows a failure. A working daemon therefore writes each step once, and a
// failing one writes it once per distinct reason, which keeps the file bounded (daemonlog.go).
func stepOutcome(step string, err error, okDetail string) {
	daemonLog.mu.Lock()
	defer daemonLog.mu.Unlock()
	if daemonLog.stepFailed == nil {
		daemonLog.stepFailed = map[string]string{}
		daemonLog.stepSeen = map[string]string{}
	}
	if err != nil {
		msg := redactText(err.Error())
		if prev, failed := daemonLog.stepFailed[step]; failed && prev == msg {
			return
		}
		daemonLog.stepFailed[step] = msg
		log.Print("vpn: " + stepBody(step, statusFailed, msg))
		return
	}
	_, wasFailed := daemonLog.stepFailed[step]
	last, seen := daemonLog.stepSeen[step]
	if seen && !wasFailed && last == okDetail {
		return
	}
	delete(daemonLog.stepFailed, step)
	daemonLog.stepSeen[step] = okDetail
	log.Print("vpn: " + stepBody(step, statusOK, okDetail))
}

// logStepLine writes a step line that was already assembled by a pure helper (the handshake watch).
func logStepLine(body string) { log.Print("vpn: " + body) }
