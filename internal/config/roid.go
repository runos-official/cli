package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

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

/*
ValidateIssuer accepts the roid issuer URL an environment signs in through.

An issuer receives refresh tokens and proofs, so it must be https. Plain http is allowed only on a
loopback host, for a roid running on the developer's own machine. The issuer is an origin: a path,
a query, a fragment or credentials in the URL would make the CLI call endpoints roid does not serve
or send a secret somewhere unintended. `runos config set auth-issuer` and the CDN config both call
this, so the two ways to name an issuer accept the same values.
*/
func ValidateIssuer(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("the issuer must be a URL with scheme and host (got %q)", value)
	}
	if u.User != nil {
		return errors.New("the issuer must not carry credentials (user or password in the URL)")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return fmt.Errorf("the issuer is an origin with no path (got %q)", value)
	}
	host := strings.ToLower(u.Hostname())
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && loopback) {
		return fmt.Errorf("the issuer must use https (http is allowed only on localhost); got %q", value)
	}
	return nil
}

// CanonicalIssuer is the one spelling of an issuer that the CLI stores and compares: no
// surrounding space, no trailing slash, scheme and host in lower case. Two spellings of the same
// origin must not make a sign-in stop counting as established.
func CanonicalIssuer(issuer string) string {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return issuer
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.EscapedPath()
}

// ApplyEnvironmentAuth records the environment's sign-in provider. Only a
// roid provider with a valid issuer selects roid; anything else, including
// an unknown provider, keeps the Firebase sign-in. A roid block whose issuer
// fails ValidateIssuer keeps Firebase and returns the reason, so the caller
// can say so once.
func (c *Config) ApplyEnvironmentAuth(a RemoteAuth) error {
	c.AuthIssuer = ""
	if a.Provider != AuthProviderRoid || a.Issuer == "" {
		return nil
	}
	if err := ValidateIssuer(a.Issuer); err != nil {
		return err
	}
	c.AuthIssuer = CanonicalIssuer(a.Issuer)
	return nil
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
	return c != nil && c.AuthIssuer != "" && c.Roid != nil && c.Roid.PersonID != "" &&
		CanonicalIssuer(c.Roid.Issuer) == CanonicalIssuer(c.AuthIssuer)
}
