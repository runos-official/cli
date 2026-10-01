package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/vpn"
	"github.com/runos-official/cli/version"
)

/*
The record `runos vpn up` leaves of its own steps (FCR349).

Sign-in, device registration and the session mint run in the CLI, so the daemon log cannot see them.
The trace is replaced on every `up` and is nil-safe, so a command that cannot write it carries on.
`runos vpn diagnose` reads it back.
*/

// upTraceFile sits in the person's own config directory, never in the root-owned daemon state.
const upTraceFile = "vpn-up.log"

// upTrace is the trace of the `up` in progress. A package variable because the steps it records are
// spread over functions that tests call directly; those tests leave it nil, which records nothing.
var upTrace *vpn.Trace

func startUpTrace() {
	dir, err := config.Dir()
	if err != nil {
		upTrace = nil
		return
	}
	upTrace = vpn.StartTrace(filepath.Join(dir, upTraceFile), version.Version)
}

// stuckHint is appended to a failed `up`, so the person learns the report exists at the moment they
// need it, and so does an assistant reading the error.
const stuckHint = "Stuck? 'runos vpn diagnose' prints a report of which step failed and why. " +
	"It is safe to paste: it contains no tokens or keys."

// vpnUpError carries the hint without changing what errors.Is and errors.As see.
type vpnUpError struct{ err error }

func (e vpnUpError) Error() string { return fmt.Sprintf("%s\n\n%s", e.err.Error(), stuckHint) }
func (e vpnUpError) Unwrap() error { return e.err }
