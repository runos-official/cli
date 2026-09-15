package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

/*
One login, several accounts.

Conductor authorises a signed-in request by MEMBERSHIP. A login may act on every account it is a
member of, whatever account its sign-in names, and that account is only the login's default landing
account. So the CLI asks conductor which accounts the login belongs to (GET /user/accounts), and
that list is the set of accounts it can switch between without a browser.

The list is advisory here. Conductor still refuses a request to an account the login is not a member
of, so an unreadable list never blocks a sign-in or a switch. It only costs the browser round trip
the list would have saved.
*/

var errNoAPIURL = errors.New("no API URL is configured")

// loadMemberships reads the accounts the login behind token is a member of.
func loadMemberships(cfg *config.Config, token string) ([]api.UserAccount, error) {
	baseURL := cfg.GetAPIURLQuiet()
	if baseURL == "" {
		return nil, errNoAPIURL
	}
	if token == "" {
		return nil, auth.ErrNotAuthenticated
	}
	return api.NewClient(baseURL).UserAccounts(token)
}

// sessionMemberships reads the memberships of a sign-in that just completed, with the ID token the
// sign-in already holds. Nil means the list could not be read, which is not the same as none.
func sessionMemberships(cfg *config.Config, session browserSession) []api.UserAccount {
	accounts, err := loadMemberships(cfg, session.IDToken)
	if err != nil {
		return nil
	}
	return accounts
}

// signedInMemberships reads the memberships of the active credential, or nil when the CLI is signed
// out or the list could not be read. An API key answers with its own account only.
func signedInMemberships(cfg *config.Config) []api.UserAccount {
	if !auth.HasCredentials(cfg) {
		return nil
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return nil
	}
	accounts, err := loadMemberships(cfg, token)
	if err != nil {
		return nil
	}
	return accounts
}

func memberAccountIDs(accounts []api.UserAccount) []string {
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.AID != "" {
			ids = append(ids, account.AID)
		}
	}
	return ids
}

func hasMembership(accounts []api.UserAccount, accountID string) bool {
	if accountID == "" {
		return false
	}
	for _, account := range accounts {
		if account.AID == accountID {
			return true
		}
	}
	return false
}

/*
verifyRequestedAccount picks the account a fresh sign-in makes active.

A sign-in lands on the login's DEFAULT account. `account switch bbbbb` whose sign-in landed on aaaaa
used to fail with "does not match", even when the login was a member of bbbbb and conductor would
serve it. A requested account now passes when it is the sign-in's own account or one of the login's
memberships, and it becomes the active account.

Nil memberships means the list could not be read. Then only the sign-in's own account is confirmed,
which is the check this made before memberships existed.
*/
func verifyRequestedAccount(requested, authenticated string, memberships []api.UserAccount) (string, error) {
	switch {
	case requested == "":
		return authenticated, nil
	case requested == authenticated || hasMembership(memberships, requested):
		return requested, nil
	case memberships == nil:
		return "", fmt.Errorf("authenticated account %q does not match requested account %q, and the CLI could not read which accounts this login is a member of", authenticated, requested)
	case len(memberships) == 0:
		return "", fmt.Errorf("the login you signed in with is not a member of account %q, or of any account", requested)
	default:
		return "", fmt.Errorf("the login you signed in with is not a member of account %q (it is a member of: %s)", requested, strings.Join(memberAccountIDs(memberships), ", "))
	}
}

/*
shareActiveSessionWith stores the active sign-in for an account its login joined after signing in.

A sign-in stores its session for every membership it had at that moment. An invite accepted later is
a membership nothing was stored for, and switching to it opened a browser only to land on the same
login. So when nothing is stored for the account and the active credential is a sign-in, the CLI asks
conductor, and a member account gets the active session stored against it.

An API key never qualifies: it is scoped to one account, and its membership list says nothing about
the sign-in the config also holds.

Reports whether it stored a session, which also means conductor already confirmed the membership.
*/
func shareActiveSessionWith(cfg *config.Config, accountID string) bool {
	if cfg.HasStoredCredential(accountID) || auth.Kind(cfg) != auth.CredentialInteractive || cfg.RefreshToken == "" {
		return false
	}
	token, err := auth.ResolveToken(cfg)
	if err != nil {
		return false
	}
	memberships, err := loadMemberships(cfg, token)
	if err != nil || !hasMembership(memberships, accountID) {
		return false
	}
	cfg.ShareSessionWithAccounts([]string{accountID}, cfg.Firebase, cfg.RefreshToken, cfg.SignedInAt)
	return true
}

/*
confirmMembership checks that the login behind a stored credential is still a member of the account
the credential is about to open.

A refresh token outlives a membership. It belongs to the login, not to the account, so a login
removed from an account keeps a token that still refreshes. Without this check the switch reported
success and the next command was refused.

An unreadable list is NOT a refusal. It is reported on warn and the switch goes ahead, because
conductor still refuses a non-member on every request, and a conductor without the list has nothing
to read.
*/
func confirmMembership(cfg *config.Config, token, accountID string, warn io.Writer) error {
	memberships, err := loadMemberships(cfg, token)
	if err != nil {
		fmt.Fprintf(warn, "Could not confirm membership of %s, so switching anyway: %v\n", accountID, err)
		return nil
	}
	if hasMembership(memberships, accountID) {
		return nil
	}
	return fmt.Errorf("the saved sign-in for %s belongs to a login that is no longer a member of that account. Run 'runos account add' to sign in with a login that is, or 'runos account forget %s --yes' to remove it", accountID, accountID)
}
