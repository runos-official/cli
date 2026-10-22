package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"
)

// issuerCheckTimeout bounds the discovery check, so a dead issuer costs a new sign-in a few
// seconds and no more.
const issuerCheckTimeout = 5 * time.Second

// checkRoidIssuer asks the issuer for a valid discovery document. A var so tests stub it.
var checkRoidIssuer = func(issuer string) error { return roidauth.CheckIssuer(issuer, issuerCheckTimeout) }

/*
useRoidSignIn decides whether `runos login` signs in through roid or through Firebase.

An environment can name an issuer in the CDN config before that issuer is live. Sending a person
there gets them a sign-in that cannot finish, so a NEW sign-in first asks the issuer for its
discovery document. If the issuer does not answer one, the person is told and signs in the
standard way instead.

Two limits keep this from weakening roid:

  - It applies only where this machine holds no roid sign-in for the issuer. An established roid
    sign-in is never replaced by Firebase because the issuer is unreachable or answers badly. The
    login goes to roid and fails where the person can see it.
  - It changes the sign-in method and nothing else. The fallback sends no credential to the
    issuer, takes none from it, and the server still decides what a Firebase token may do.

A network attacker who can block the issuer can therefore make a NEW sign-in use Firebase, the
sign-in every earlier release used. It gains no credential by doing so.
*/
func useRoidSignIn(cfg *config.Config, notice io.Writer) bool {
	if cfg.AuthIssuer == "" {
		return false
	}
	if cfg.HasRoidSignIn() {
		return true
	}
	if err := checkRoidIssuer(cfg.AuthIssuer); err != nil {
		fmt.Fprintf(notice, "Note: the RunOS identity service is not available (%v).\n", err)
		fmt.Fprintf(notice, "Signing in with the standard sign-in. Run 'runos login' again once the identity service is live to use it.\n")
		return false
	}
	return true
}
