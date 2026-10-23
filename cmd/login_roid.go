package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
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
signInOptions are the few things a caller can ask of a sign-in.

requireSamePerson is for the sign-in check a VPN session needs (`runos vpn up`): the check is for
the person who is signed in, so an approval by anybody else is refused and nothing changes.
`runos login` leaves it off, because signing in as someone else is a legitimate reason to run it.
*/
type signInOptions struct {
	requireSamePerson bool
}

/*
roidLoginReporting signs in through roid, the RunOS identity service, for an environment whose
config names an auth issuer.

The device flow runs against roid itself; nothing passes through conductor. Every token is bound
to a fresh proof key, and the key and the refresh token go to the OS credential store. config.json
records only the issuer and the person.

A previous roid sign-in on this machine is NOT touched until the new one is approved, stored and
saved. It used to be revoked first, so a check that was cancelled, expired or killed before approval
left the person signed out of the CLI, the Console and the Desktop app (FCR 1562). Now it is
retired afterwards, so exactly one is live when this returns and a sign-in that never finished
changes nothing.
*/
func roidLoginReporting(cmd *cobra.Command, cfg *config.Config, report signInReporter, chatty, autoOpenBrowser bool, opts signInOptions) error {
	previousAccountID := cfg.AccountID
	held := holdPreviousRoidSignIn(cmd, cfg)
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
	if err := refuseDifferentPerson(held, session, id.PersonID, opts.requireSamePerson, cmd.ErrOrStderr()); err != nil {
		report.Failed("different_person", err.Error())
		return err
	}
	report.Authorized()

	account := pickRoidAccount(previousAccountID, id.Accounts)
	cfg.ApplyRoidLogin(cfg.AuthIssuer, id.PersonID, account, time.Now().UTC().Format(time.RFC3339))
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("signed in, but could not save the config: %w", err)
	}
	held.retire(id.PersonID, cfg.AuthIssuer, cmd.ErrOrStderr())
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

// replaceableSignIn is what a new sign-in needs of the one it replaces. *roidauth.Session is one.
type replaceableSignIn interface {
	SignedInPerson() string
	Release()
	RevokeCaptured(token []byte) error
	Logout() error
}

/*
heldSignIn is the sign-in a new one will replace, kept live while the new one is being approved.

refresh is its refresh token as it was BEFORE the new sign-in. The new sign-in of the same person on
the same issuer writes to the same credential-store slots, so the old token is gone from the store
once the new one is saved, while the server still holds its token family and must be told.
*/
type heldSignIn struct {
	session replaceableSignIn
	person  string
	issuer  string
	refresh []byte
}

// holdPreviousRoidSignIn opens the sign-in config.json records, the way endRoidSignIn does (against
// the issuer it was made with), and reads its refresh token. It changes nothing. Nil when there is
// no recorded sign-in or its store cannot be opened (the new sign-in then has nothing to retire).
func holdPreviousRoidSignIn(cmd *cobra.Command, cfg *config.Config) *heldSignIn {
	if cfg == nil || cfg.Roid == nil || cfg.Roid.PersonID == "" || cfg.Roid.Issuer == "" {
		return nil
	}
	recorded := cfg.Clone()
	recorded.AuthIssuer = cfg.Roid.Issuer
	s, err := auth.RoidSession(&recorded)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not open the credential store to read the previous sign-in: %v\n", err)
		return nil
	}
	return &heldSignIn{session: s, person: cfg.Roid.PersonID, issuer: cfg.Roid.Issuer, refresh: s.CaptureRefresh()}
}

// retire ends the held sign-in after the new one is approved, stored and saved. Best effort: a
// revoke the server did not confirm is a warning, never a failed sign-in.
func (h *heldSignIn) retire(newPerson, newIssuer string, warn io.Writer) {
	if h == nil || h.session == nil {
		return
	}
	if h.person == newPerson && h.issuer == newIssuer {
		// Same slots: the new secrets are there now, so only this process's memory is dropped and
		// the old token family is revoked on the server. A Logout here would delete the new secrets.
		h.session.Release()
		if err := h.session.RevokeCaptured(h.refresh); err != nil {
			fmt.Fprintf(warn, "Warning: %v\n", err)
		}
		return
	}
	// Other slots: they belong to the old sign-in alone.
	if err := h.session.Logout(); err != nil {
		fmt.Fprintf(warn, "Warning: %v\n", err)
	}
}

// errDifferentApprover is the plain refusal for a sign-in check approved by someone else.
const errDifferentApprover = "you approved as a different account holder than the one signed in here. Nothing was changed. Open the sign-in link in the browser that is signed in as the right person, then try again"

// refuseDifferentPerson is for the sign-in check a VPN session needs. When the approver is not the
// person signed in here, it ends the NEW sign-in and returns the refusal, so the old sign-in and the
// running tunnel stay exactly as they were. With require off, or nothing held, it does nothing.
func refuseDifferentPerson(h *heldSignIn, newSession replaceableSignIn, newPerson string, require bool, warn io.Writer) error {
	if !require || h == nil || h.person == "" || h.person == newPerson {
		return nil
	}
	if err := newSession.Logout(); err != nil {
		fmt.Fprintf(warn, "Warning: the sign-in made by the other person could not be fully ended: %v\n", err)
	}
	return errors.New(errDifferentApprover)
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
