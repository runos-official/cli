package cmd

import "fmt"

/*
What to tell someone after a successful sign-in.

WHY THIS EXISTS. Login reported only "Authenticated successfully!" and never
named the account. `runos login` always opens the window, because signing into a
DIFFERENT account is a legitimate reason to run it, so the account you end up on
is genuinely in question every time. Not saying which one is silence about the
only thing that varies.

MEASURED 2026-09-13. An operator signed in to recover an expired provider
session, authenticated as the tenant instead, and was told it succeeded. Every
following command still failed. Several rounds went into diagnosing an API that
was healthy the whole time, and the answer was one word the CLI already knew and
did not print.
*/
func loginOutcomeMessage(previousAccountID, signedInAccountID string) string {
	if signedInAccountID == "" {
		return "Authenticated successfully!"
	}
	if previousAccountID == "" || previousAccountID == signedInAccountID {
		return fmt.Sprintf("Authenticated successfully! Signed in as %s.", signedInAccountID)
	}
	// Naming BOTH, because switching account is the case where the reader is
	// most likely to have meant something else.
	return fmt.Sprintf(
		"Authenticated successfully! Signed in as %s, switched from %s.\n"+
			"The sign-in for %s is kept; `runos account switch %s` goes back to it.",
		signedInAccountID, previousAccountID, previousAccountID, previousAccountID)
}
