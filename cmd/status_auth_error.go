package cmd

import (
	"errors"
	"net"
	"net/url"
	"regexp"
)

/*
Why a failed token refresh has a KIND (FCR160).

Two things end a Firebase refresh and they are not the same fact:

  - The request never completed. A timeout, a dead link, DNS. This says nothing at all about the
    sign-in, and the remedy is the network. It is usually over by the next poll.
  - Google refused it. The sign-in is genuinely gone and the remedy is the browser.

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
	if err == nil {
		return "", ""
	}
	// Every transport failure from net/http arrives wrapped in one of these two. Matching on the
	// TYPE rather than on the text is what keeps this working when Go rewords a timeout.
	var urlErr *url.Error
	var netErr *net.OpError
	if errors.As(err, &urlErr) || errors.As(err, &netErr) {
		return authErrorNetwork, "Could not reach the sign-in service. Check your connection; your sign-in is unaffected."
	}
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
