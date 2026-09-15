package config

// LeaveOutcome says what leaving an account did to the active account.
type LeaveOutcome int

const (
	// LeaveKeptActive means the login left an account the config is not on. The active account stays.
	LeaveKeptActive LeaveOutcome = iota
	// LeaveMovedToDefault means the login left the active account, and its session now opens the
	// login's default account.
	LeaveMovedToDefault
	// LeaveSignedOut means the login left the active account and has no default account to move to.
	LeaveSignedOut
)

/*
LeaveAccount updates the config after the signed-in login left an account.

The stored sign-in for the left account is removed, because it opens nothing now. A stored API key for
that account is kept: a personal access token is not the login, and ShareSessionWithAccounts keeps it
for the same reason.

When the login left the ACTIVE account, the same session moves to defaultAccountID, which conductor has
just made the login's default landing account. With no default account there is nothing to move to.
Conductor then clears the claim and revokes the login's refresh tokens, so the session is cleared.

That sign-out is narrower than ClearSession. It clears only the stored credentials that carry the
revoked refresh token. An API key and another login's sign-in still work, and leaving one account did
not touch them.
*/
func (c *Config) LeaveAccount(leftAccountID, defaultAccountID, usedAt string) LeaveOutcome {
	if c == nil || leftAccountID == "" {
		return LeaveKeptActive
	}
	session := c.RefreshToken
	if index := c.knownAccountIndex(leftAccountID); index >= 0 && c.KnownAccounts[index].RefreshToken != "" {
		c.forgetStoredSession(index)
	}
	if c.AccountID != leftAccountID {
		return LeaveKeptActive
	}
	if defaultAccountID != "" && defaultAccountID != leftAccountID && session != "" {
		c.forgetDefaultClusterOnAccountChange(defaultAccountID)
		c.AccountID = defaultAccountID
		c.ShareSessionWithAccounts([]string{defaultAccountID}, c.Firebase, session, c.SignedInAt)
		c.RememberAccount(defaultAccountID, usedAt)
		return LeaveMovedToDefault
	}
	c.RefreshToken, c.Firebase, c.APIKey = "", nil, ""
	c.AccountID, c.SignedInAt, c.DefaultClusterID = "", "", ""
	c.ClearActiveAccount()
	for i := range c.KnownAccounts {
		if session != "" && c.KnownAccounts[i].RefreshToken == session {
			c.forgetStoredSession(i)
		}
	}
	return LeaveSignedOut
}

func (c *Config) forgetStoredSession(index int) {
	c.KnownAccounts[index].RefreshToken = ""
	c.KnownAccounts[index].Firebase = nil
	c.KnownAccounts[index].SignedInAt = ""
}
