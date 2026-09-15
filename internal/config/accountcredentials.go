package config

import (
	"sort"
	"time"
)

// KnownAccount stores an account that used this CLI: its metadata, and the
// credential that opens it without a new sign-in.
type KnownAccount struct {
	AccountID  string `json:"account_id"`
	AddedAt    string `json:"added_at"`
	LastUsedAt string `json:"last_used_at"`
	Active     bool   `json:"active"`
	/*
	   The credential for THIS account, kept so switching back does not mean
	   signing in again.

	   Before this, the config held one refresh token. Switching account
	   overwrote it, so moving to a second account signed you out of the first
	   and every switch back meant another browser round-trip, even seconds
	   later with a token that had an hour left on it. Reported by an operator
	   moving between a provider account and a tenant account, which is a
	   thing this product asks people to do.

	   Exactly one of these is ever set, matching the active pair: a refresh
	   token is a session and an API key is not, and holding both for one
	   account is the state ApplySessionLogin and ApplyAPIKeyLogin exist to
	   prevent.

	   Stored in the same 0600 file as the active credential, because that is
	   what it is: the same secret, for an account that is not current.
	*/
	RefreshToken string `json:"refresh_token,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	SignedInAt   string `json:"signed_in_at,omitempty"`
	/*
	   The Firebase project the refresh token belongs to. A refresh token is
	   only spendable against its own project, so switching to this account
	   restores it beside the token. Without it, a switch reused whatever
	   project the previous account had set.

	   Nil on a record written before this field existed. A switch then keeps
	   the active project, which was the only behaviour before.
	*/
	Firebase *FirebaseConfig `json:"firebase,omitempty"`
}

// RememberAccount adds an account or marks an existing account as active.
func (c *Config) RememberAccount(accountID, usedAt string) {
	if c == nil || accountID == "" {
		return
	}
	if usedAt == "" {
		usedAt = time.Now().UTC().Format(time.RFC3339)
	}
	found := false
	for i := range c.KnownAccounts {
		c.KnownAccounts[i].Active = c.KnownAccounts[i].AccountID == accountID
		if c.KnownAccounts[i].AccountID == accountID {
			found = true
			c.KnownAccounts[i].LastUsedAt = usedAt
			if c.KnownAccounts[i].AddedAt == "" {
				c.KnownAccounts[i].AddedAt = usedAt
			}
		}
	}
	if !found {
		c.KnownAccounts = append(c.KnownAccounts, KnownAccount{
			AccountID: accountID, AddedAt: usedAt, LastUsedAt: usedAt, Active: true,
		})
	}
	sort.SliceStable(c.KnownAccounts, func(i, j int) bool {
		return c.KnownAccounts[i].AddedAt < c.KnownAccounts[j].AddedAt
	})
}

// rememberCredential stores one account's credential beside its metadata.
func (c *Config) rememberCredential(accountID, refreshToken, apiKey string, firebase *FirebaseConfig, signedInAt string) {
	if c == nil || accountID == "" {
		return
	}
	for i := range c.KnownAccounts {
		if c.KnownAccounts[i].AccountID != accountID {
			continue
		}
		c.KnownAccounts[i].RefreshToken = refreshToken
		c.KnownAccounts[i].APIKey = apiKey
		c.KnownAccounts[i].Firebase = copyFirebase(firebase)
		c.KnownAccounts[i].SignedInAt = signedInAt
	}
}

/*
ShareSessionWithAccounts stores one login's session as the credential for every account that login
is a member of, so switching to any of them needs no browser.

One Firebase login can be a member of several accounts, and conductor authorises each request by
membership, not by the account the sign-in names. The same refresh token therefore opens all of
them. Before this, the CLI stored the token against the one account the sign-in named, and every
other member account cost a browser round trip.

It does NOT change the active account. An account the CLI has never seen is added with no
last-used time, because nobody has used it here yet.

A stored API key is kept. A personal access token is a deliberate per-account choice that outlives
any session, and overwriting it with a session would change which identity acts on that account.
*/
func (c *Config) ShareSessionWithAccounts(accountIDs []string, firebase *FirebaseConfig, refreshToken, signedInAt string) {
	if c == nil || refreshToken == "" {
		return
	}
	for _, accountID := range accountIDs {
		if accountID == "" {
			continue
		}
		index := c.knownAccountIndex(accountID)
		if index < 0 {
			c.KnownAccounts = append(c.KnownAccounts, KnownAccount{AccountID: accountID, AddedAt: signedInAt})
			index = len(c.KnownAccounts) - 1
		}
		if c.KnownAccounts[index].APIKey != "" {
			continue
		}
		c.KnownAccounts[index].RefreshToken = refreshToken
		c.KnownAccounts[index].Firebase = copyFirebase(firebase)
		c.KnownAccounts[index].SignedInAt = signedInAt
	}
	sort.SliceStable(c.KnownAccounts, func(i, j int) bool {
		return c.KnownAccounts[i].AddedAt < c.KnownAccounts[j].AddedAt
	})
}

func (c *Config) knownAccountIndex(accountID string) int {
	for i := range c.KnownAccounts {
		if c.KnownAccounts[i].AccountID == accountID {
			return i
		}
	}
	return -1
}

// copyFirebase copies the settings so two accounts never share one pointer that a later edit
// could change for both.
func copyFirebase(firebase *FirebaseConfig) *FirebaseConfig {
	if firebase == nil {
		return nil
	}
	copied := *firebase
	return &copied
}

/*
ActivateStoredAccount makes a remembered account current WITHOUT signing in again.

Reports false when there is nothing stored for that account, which is the
caller's signal to fall back to the browser. It does NOT report whether the
credential still works: a refresh token can be revoked or expire, and finding
that out costs a network call the caller makes anyway. So the caller activates,
tries, and falls back on failure.

The default cluster is dropped on a real account change for the same reason it
is on a sign-in: cluster ids are scoped to an account, so one carried across is
not stale, it is guaranteed wrong.

The Firebase project is restored with a session credential, because a refresh
token is only spendable against the project that issued it. A record with no
stored project keeps the active one, which is what every switch did before.
*/
func (c *Config) ActivateStoredAccount(accountID, usedAt string) bool {
	if c == nil || accountID == "" {
		return false
	}
	for i := range c.KnownAccounts {
		account := c.KnownAccounts[i]
		if account.AccountID != accountID {
			continue
		}
		if account.RefreshToken == "" && account.APIKey == "" {
			return false
		}
		c.forgetDefaultClusterOnAccountChange(accountID)
		c.AccountID = accountID
		c.RefreshToken = account.RefreshToken
		c.APIKey = account.APIKey
		if account.RefreshToken != "" && account.Firebase != nil {
			c.Firebase = copyFirebase(account.Firebase)
		}
		if account.SignedInAt != "" {
			c.SignedInAt = account.SignedInAt
		}
		c.RememberAccount(accountID, usedAt)
		return true
	}
	return false
}

// HasStoredCredential reports whether switching to an account could skip the
// browser. Present so a caller can say so before trying, rather than after.
func (c *Config) HasStoredCredential(accountID string) bool {
	if c == nil {
		return false
	}
	for _, account := range c.KnownAccounts {
		if account.AccountID == accountID {
			return account.RefreshToken != "" || account.APIKey != ""
		}
	}
	return false
}

// ClearActiveAccount preserves known accounts and clears their active state.
func (c *Config) ClearActiveAccount() {
	for i := range c.KnownAccounts {
		c.KnownAccounts[i].Active = false
	}
}

// ForgetAccount removes one account from local metadata.
func (c *Config) ForgetAccount(accountID string) bool {
	found := false
	kept := c.KnownAccounts[:0]
	for _, account := range c.KnownAccounts {
		if account.AccountID == accountID {
			found = true
			continue
		}
		kept = append(kept, account)
	}
	c.KnownAccounts = kept
	return found
}
