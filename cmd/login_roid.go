package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"

	"github.com/spf13/cobra"
)

// roidTextSignIn is the terminal reporter for a roid sign-in. roid's
// approval page asks the person to type the code from the terminal, so
// the code line says that instead of "verify this matches".
type roidTextSignIn struct{ textSignIn }

func (t roidTextSignIn) DeviceCode(userCode, _ string) {
	fmt.Fprintf(t.out, "Opening browser to sign in...\n")
	fmt.Fprintf(t.out, "Code: %s - type this code in the browser to approve this sign-in\n", userCode)
}

/*
pickRoidAccount chooses the account the CLI addresses after a roid sign-in.

The previous account wins while the person is still a member of it, so signing in again never
moves a working setup to another account. Otherwise the first account in a stable order. A person
in no account yet gets none; the sign-in still stands.
*/
func pickRoidAccount(previous string, accounts []string) string {
	sorted := append([]string(nil), accounts...)
	sort.Strings(sorted)
	for _, a := range sorted {
		if a == previous {
			return a
		}
	}
	if len(sorted) == 0 {
		return ""
	}
	return sorted[0]
}

/*
roidLoginReporting signs in through roid, the RunOS identity service, for an environment whose
config names an auth issuer.

The device flow runs against roid itself; nothing passes through conductor. Every token is bound
to a fresh proof key, and the key and the refresh token go to the OS credential store. config.json
records only the issuer and the person. A previous roid sign-in on this machine is revoked first,
so exactly one is live.
*/
func roidLoginReporting(cmd *cobra.Command, cfg *config.Config, report signInReporter, chatty, autoOpenBrowser bool) error {
	previousAccountID := cfg.AccountID
	endRoidSignIn(cmd, cfg)
	fresh := cfg.Clone()
	fresh.Roid = nil
	session, err := auth.RoidSession(&fresh)
	if err != nil {
		report.Failed("credential_store", err.Error())
		return err
	}
	session.OnPending = report.Pending
	id, err := session.Login(context.Background(), func(d roidauth.DeviceStart) {
		link := d.VerificationURIComplete
		if link == "" {
			link = d.VerificationURI
		}
		report.DeviceCode(d.UserCode, link)
		if autoOpenBrowser {
			time.Sleep(browserOpenDelay)
			report.BrowserOpened(openBrowser(link) == nil, link)
		} else {
			report.BrowserOpened(false, link)
		}
	})
	if err != nil {
		var oe *roidauth.Error
		reason := "failed"
		if errors.As(err, &oe) {
			reason = oe.Code
		}
		report.Failed(reason, err.Error())
		return fmt.Errorf("sign-in failed: %w", err)
	}
	report.Authorized()

	account := pickRoidAccount(previousAccountID, id.Accounts)
	cfg.ApplyRoidLogin(cfg.AuthIssuer, id.PersonID, account, time.Now().UTC().Format(time.RFC3339))
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("signed in, but could not save the config: %w", err)
	}
	reportVPNAccountChange(cmd, previousAccountID, cfg.AccountID)
	if chatty {
		fmt.Fprintf(os.Stdout, "\nSigned in as %s.\n", id.PersonID)
		switch {
		case account == "":
			fmt.Println("You are not a member of any account yet.")
		case len(id.Accounts) > 1:
			fmt.Printf("Using account %s (%d accounts in total).\n", account, len(id.Accounts))
		default:
			fmt.Printf("Using account %s.\n", account)
		}
		warmManifestCache(cfg)
	}
	return nil
}

/*
endRoidSignIn revokes this machine's recorded roid sign-in on the server and deletes its secrets
from the credential store. Every path that replaces or ends a sign-in calls it, so no live refresh
token is left behind in the keychain when config.json stops naming it.

It ends the sign-in config.json RECORDS, against the issuer that sign-in was made with, and not
only one the environment names today. `runos config env <name>` for an environment with no issuer
(the CDN block removed, or another environment) clears the issuer and keeps the record, so that
switching back restores the sign-in; the next logout or Firebase sign-in must still revoke it.

Best effort on the server side: a revoke the server did not confirm is a warning, because the local
secrets are gone either way and the person is signing out or in on purpose.
*/
func endRoidSignIn(cmd *cobra.Command, cfg *config.Config) {
	if cfg == nil || cfg.Roid == nil || cfg.Roid.PersonID == "" || cfg.Roid.Issuer == "" {
		return
	}
	recorded := cfg.Clone()
	recorded.AuthIssuer = cfg.Roid.Issuer
	s, err := auth.RoidSession(&recorded)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not open the credential store to remove the previous sign-in: %v\n", err)
		return
	}
	if err := s.Logout(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
	}
}

// leftoverRoidNote says what a person is left with after `runos config env` moved them to an
// environment that does not use the issuer their roid sign-in was made with: signed out here, the
// sign-in recorded and its secrets in the credential store, until they sign out or in again.
func leftoverRoidNote(cfg *config.Config) string {
	if cfg == nil || cfg.Roid == nil || cfg.Roid.PersonID == "" || cfg.HasRoidSignIn() {
		return ""
	}
	return fmt.Sprintf("Note: this machine still holds a sign-in through the identity service at %s, which this environment does not use, so you are signed out here. Switching back restores it. Run 'runos logout' to end it, or 'runos login' to sign in through Firebase (that ends it too).\n", cfg.Roid.Issuer)
}
