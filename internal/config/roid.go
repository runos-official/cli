package config

// RemoteAuth is an environment's sign-in provider in the CDN config. An
// environment with no auth block signs in through Firebase.
type RemoteAuth struct {
	Provider string `json:"provider,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
}

// AuthProviderRoid names the RunOS identity service as the sign-in provider.
const AuthProviderRoid = "roid"

// RoidSignIn records a sign-in through roid. It names the issuer and the
// person only: the refresh token and the proof key live in the OS
// credential store (internal/roidauth), never in config.json.
type RoidSignIn struct {
	Issuer     string `json:"issuer"`
	PersonID   string `json:"person_id"`
	SignedInAt string `json:"signed_in_at,omitempty"`
}

// ApplyEnvironmentAuth records the environment's sign-in provider. Only a
// roid provider with an issuer selects roid; anything else, including an
// unknown provider, keeps the Firebase sign-in.
func (c *Config) ApplyEnvironmentAuth(a RemoteAuth) {
	if a.Provider == AuthProviderRoid && a.Issuer != "" {
		c.AuthIssuer = a.Issuer
		return
	}
	c.AuthIssuer = ""
}

// ApplyRoidLogin records a completed roid sign-in and clears every other
// credential, so exactly one is live. accountID may be empty when the
// person belongs to no account yet.
func (c *Config) ApplyRoidLogin(issuer, personID, accountID, signedInAt string) {
	c.forgetDefaultClusterOnAccountChange(accountID)
	c.Roid = &RoidSignIn{Issuer: issuer, PersonID: personID, SignedInAt: signedInAt}
	c.AccountID = accountID
	c.SignedInAt = signedInAt
	c.RefreshToken = ""
	c.APIKey = ""
	c.Firebase = nil
	if accountID != "" {
		c.RememberAccount(accountID, signedInAt)
	}
}

// HasRoidSignIn reports whether a roid sign-in exists for the issuer the
// environment uses now. A sign-in made against another issuer does not
// count, so switching environments never sends one environment's tokens
// to another.
func (c *Config) HasRoidSignIn() bool {
	return c != nil && c.AuthIssuer != "" && c.Roid != nil && c.Roid.Issuer == c.AuthIssuer && c.Roid.PersonID != ""
}
