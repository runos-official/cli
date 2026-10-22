package cmd

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"
)

// issuerCheckTimeout bounds the discovery check, so a dead issuer costs a new sign-in a few
// seconds and no more.
const issuerCheckTimeout = 5 * time.Second

// discoverIssuer is the discovery request itself. A var so a test sees the timeout it is given.
var discoverIssuer = roidauth.CheckIssuer

// checkRoidIssuer asks the issuer for a valid discovery document. A var so tests stub it.
var checkRoidIssuer = func(issuer string) error { return discoverIssuer(issuer, issuerCheckTimeout) }

// signInNoticer is a reporter that can carry a notice that is not a failure. Only the JSON stream
// does: RunOS Desktop keeps the CLI's stderr aside and shows it only when the command fails, so a
// notice written there alone is never seen on a sign-in that succeeds.
type signInNoticer interface{ Notice(reason, message string) }

/*
useRoidSignIn decides whether `runos login` signs in through roid or through Firebase.

An environment can name an issuer in the CDN config before that issuer is live. Sending a person
there gets them a sign-in that cannot finish, so a NEW sign-in first asks the issuer for its
discovery document, and there are three outcomes:

  - The issuer answers a valid document: roid.
  - The issuer's host answers HTTP 404 or 410 for the document. The identity service is not
    deployed there, which is prod until roid ships. The person is told, on stderr and in the JSON
    stream, and signs in the standard way.
  - Anything else (a timeout, a reset, a refused connection, a TLS error, a 5xx, a redirect, a
    document that does not parse or does not match) is an error. The sign-in stops and names the
    issuer. It does NOT fall back: a person on a filtered or hostile network must not be moved from
    a proof-bound sign-in to a bearer Firebase refresh token because someone dropped packets to the
    issuer. They retry later, or choose Firebase on purpose with `runos config set auth-issuer
    firebase`.

The check applies only where this machine holds no roid sign-in for the issuer. An established roid
sign-in is never replaced by Firebase because the issuer is unreachable or answers badly. The
login goes to roid and fails where the person can see it.

The fallback changes the sign-in method and nothing else. It sends no credential to the issuer,
takes none from it, and the server still decides what a Firebase token may do.
*/
func useRoidSignIn(cfg *config.Config, report signInReporter, notice io.Writer) (bool, error) {
	if cfg.AuthIssuer == "" {
		return false, nil
	}
	if cfg.HasRoidSignIn() {
		return true, nil
	}
	err := checkRoidIssuer(cfg.AuthIssuer)
	if err == nil {
		return true, nil
	}
	var absent *roidauth.IssuerNotDeployedError
	if errors.As(err, &absent) {
		fmt.Fprintf(notice, "Note: the RunOS identity service is not available (%v).\n", err)
		fmt.Fprintf(notice, "Signing in with the standard sign-in. Run 'runos login' again once the identity service is live to use it.\n")
		if n, ok := report.(signInNoticer); ok {
			n.Notice("identity_service_not_deployed", fmt.Sprintf("The RunOS identity service is not deployed at %s. Signing in with the standard sign-in.", absent.Issuer))
		}
		return false, nil
	}
	failure := fmt.Errorf("could not check the RunOS identity service at %s: %w.\nTry again later, or run 'runos config set auth-issuer firebase' to sign in through Firebase instead", config.CanonicalIssuer(cfg.AuthIssuer), err)
	report.Failed("identity_service_unavailable", failure.Error())
	return false, failure
}
