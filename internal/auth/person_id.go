package auth

import "github.com/runos-official/cli/internal/config"

// PersonID is the id of the person this CLI is signed in as, or "" when the
// credential is an API key or there is no sign-in. token is the bearer that
// ResolveToken returned for cfg.
//
// Each sign-in kind records the person in a different place. A Firebase ID
// token carries the id in its payload. An identity service access token does
// not, so the id comes from the config, where the sign-in stored it. A caller
// that reads only the token takes an identity service sign-in for an API key.
func PersonID(cfg *config.Config, token string) string {
	switch Kind(cfg) {
	case CredentialRoid:
		return cfg.Roid.PersonID
	case CredentialInteractive:
		return ExtractFirebaseUID(token)
	default:
		return ""
	}
}
