package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/runos-official/cli/internal/api"
	"github.com/spf13/cobra"
)

/*
The version gate's refusal is printed in one place, Execute, as one plain sentence.

Every client sets api.NewTransport(), which turns the gate's 426 into an *api.UpdateRequiredError.
By the time it reaches a command it sits inside the caller's own wrap and net/http's *url.Error, so
printed as it is the person would read "request failed: Get <url>: ...". The error can come from any
hook of any command (a PersistentPreRunE, a PreRunE, a RunE, a command added late), and cobra prints
its own raw line for every one of them before Execute sees the error.

So installGateObserver tells the transport to silence cobra the moment a request is refused, and
printGateRefusal, called by Execute on whatever ExecuteC returned, prints "Error: <sentence>" (the
form docs/desktop-contract.md pins) once. A command in --json mode has already printed its envelope
and silenced itself, so it is not told twice. A command with only a Run function cannot return an
error and prints its own. The CLI does not update itself: the sentence names `runos update`.
*/
func installGateObserver(root *cobra.Command) {
	api.SetUpdateRequiredObserver(func() {
		root.SilenceErrors = true
		root.SilenceUsage = true
	})
}

// printGateRefusal prints the gate's sentence for err and reports whether err was the gate's. The
// executed command's own silence means it already printed its JSON error. The root's silence is
// the observer's and says nothing about that.
func printGateRefusal(executed, root *cobra.Command, err error, stderr io.Writer) bool {
	var gate *api.UpdateRequiredError
	if !errors.As(err, &gate) {
		return false
	}
	if executed == nil || executed == root || !executed.SilenceErrors {
		fmt.Fprintln(stderr, "Error: "+gate.Error())
	}
	return true
}

// reportGateRefusal tells a sign-in's reporter that the version gate stopped it, with the plain
// sentence and not the request URL, and says whether it was the gate.
func reportGateRefusal(report signInReporter, err error) bool {
	var gate *api.UpdateRequiredError
	if !errors.As(err, &gate) {
		return false
	}
	report.Failed("update_required", gate.Error())
	return true
}
