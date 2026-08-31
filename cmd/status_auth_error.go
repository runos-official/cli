package cmd

import (
	"errors"
	"net"
	"net/url"
	"regexp"

	"github.com/runos-official/cli/internal/auth"
)

/*
Why a failed token refresh has a KIND (FCR160).

Two things end a Firebase refresh and they are not the same fact:

  - The credential was never judged. A timeout, a dead link, DNS, a wifi portal answering with its
    own page, a proxy refusing to forward, Google itself failing. This says nothing at all about
    the sign-in, and the remedy is the network. It is usually over by the next poll.
  - It was judged and refused. The sign-in is genuinely gone and the remedy is the browser.

WHICH ONE IT WAS IS DECIDED IN `internal/auth`, not here. This function only maps the tag that
package attaches onto a kind and a sentence. It used to decide for itself, from the error's TEXT,
which worked for a dead link and failed for everything that answers: a portal returning a 200 and a
web page produced "Your session has ended", which is the sentence FCR160 was raised about.

Reporting both as "not signed in" is how a ten second wifi blip became a menu-bar banner reading
`request failed: Post "https://securetoken.googleapis.com/v1/token?key=...": context deadline
exceeded`, which named neither the problem nor anything to do about it, and sent the reader looking
for a conductor fault. Conductor is not in this path and never was.

A client branches on the kind. Never on the sentence: a message is written for a person and is
expected to be reworded, so a caller matching on it breaks silently the first time it improves.
*/
const (
	authErrorNetwork  = "network"
	authErrorRejected = "rejected"
)

// classifyAuthError says which of the two happened, and what to tell the person.
func classifyAuthError(err error) (kind, message string) {
	switch {
	case err == nil:
		return "", ""

	// Something ANSWERED and it was not the sign-in service: a wifi portal, a proxy, a filtering
	// appliance. Its own advice is different from a dead link's, because "check your connection"
	// is no use to somebody whose connection works and whose hotel wants them to accept its terms.
	case errors.Is(err, auth.ErrInterceptedReply):
		return authErrorNetwork, "Something on this network answered instead of the sign-in service. " +
			"If you are on wifi that needs its own sign-in page, complete that first. Your RunOS sign-in is unaffected."

	case errors.Is(err, auth.ErrNetworkUnreachable):
		return authErrorNetwork, "Could not reach the sign-in service. Check your connection; your sign-in is unaffected."

	// Google will not take this MACHINE's Firebase settings, whoever is signed in. Sending somebody
	// to a browser for it is a loop with no exit, so the sentence names the setting instead.
	case errors.Is(err, auth.ErrClientMisconfigured):
		return authErrorRejected, "This machine's sign-in settings were refused, so signing in again will not help. " +
			"Check which environment the CLI is pointed at with 'runos config get'."

	case errors.Is(err, auth.ErrCredentialRefused):
		return authErrorRejected, "Your session has ended. Run 'runos login' to sign in again."
	}

	// A backstop for errors raised outside `internal/auth`, which carry no tag. Every transport
	// failure from net/http arrives wrapped in one of these two, and matching on the TYPE rather
	// than on the text is what keeps this working when Go rewords a timeout.
	var urlErr *url.Error
	var netErr *net.OpError
	if errors.As(err, &urlErr) || errors.As(err, &netErr) {
		return authErrorNetwork, "Could not reach the sign-in service. Check your connection; your sign-in is unaffected."
	}
	// Anything else, including ErrNotAuthenticated, is a credential the machine cannot use. Erring
	// towards "sign in again" is deliberate: it names a remedy, where erring the other way leaves
	// somebody watching a status that never improves.
	return authErrorRejected, "Your session has ended. Run 'runos login' to sign in again."
}

// apiKeyInQuery matches the Firebase key Google puts on the query string. It is a public client
// key rather than a secret, but `runos status --json` gets pasted into chat windows and a key in
// one of those is noise at best.
var apiKeyInQuery = regexp.MustCompile(`([?&]key=)[^&"\s]+`)

// redactAPIKey keeps the diagnostic detail worth having, which is the host and the failure, and
// drops the key on the end of it.
func redactAPIKey(detail string) string {
	return apiKeyInQuery.ReplaceAllString(detail, "${1}REDACTED")
}
