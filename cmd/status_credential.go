package cmd

import (
	"fmt"
	"strings"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// authMethod names the credential SOURCE the CLI would use for outgoing
// requests, in auth.ResolveToken's priority order. authNone means no
// credential is present. Like auth.CredentialKind, this is a transport
// question only: authEnvKey and authStoredKey both cover a Personal
// Access Token AND an Account API Key, and neither name claims which
// one the secret actually is. Story 343 / FCR2030: the prior names
// (authPATEnv, authPATStored) baked "PAT" into the source label itself,
// so an Account API Key read as an authenticated PAT everywhere this
// value reached a person.
type authMethod string

const (
	authNone      authMethod = "none"
	authEnvKey    authMethod = "env-key"    // RUNOS_API_KEY
	authStoredKey authMethod = "stored-key" // cfg.APIKey, set by `runos login --api-key`
	authRoid      authMethod = "roid"       // browser sign-in through roid
	authFirebase  authMethod = "firebase"   // interactive refresh-token session
)

// resolveAuthMethod reports which credential SOURCE the CLI would use,
// mirroring auth.ResolveToken's priority (RUNOS_API_KEY -> stored
// secret -> Firebase) without any network round-trip, so `runos status`
// recognises a stored secret credential as authenticated instead of
// only honouring the Firebase fields. apiKeyEnv is passed in (the
// RUNOS_API_KEY value) to keep the helper pure.
func resolveAuthMethod(cfg *config.Config, apiKeyEnv string) authMethod {
	if strings.TrimSpace(apiKeyEnv) != "" {
		return authEnvKey
	}
	if cfg != nil && strings.TrimSpace(cfg.APIKey) != "" {
		return authStoredKey
	}
	if cfg.HasRoidSignIn() {
		return authRoid
	}
	if cfg != nil && cfg.RefreshToken != "" && cfg.Firebase != nil {
		return authFirebase
	}
	return authNone
}

// isSecretMethod reports whether method is a bearer secret source
// (env or stored), the SAME source question auth.CredentialKind.
// IsSecretCredential answers, kept here as its own predicate because
// authMethod is this package's own enum, not auth.CredentialKind.
func isSecretMethod(method authMethod) bool {
	return method == authEnvKey || method == authStoredKey
}

// statusCredentialType reports the "credentialType" value `runos
// status --json` should carry for an authenticated secret credential:
// always CredentialTypeUnknown today. No endpoint this command calls
// returns a trusted, verified semantic type for the credential in use,
// so the honest answer is "unknown", never a guessed "personal_pat".
// Returns "" for a non-secret method (roid, firebase, none), where the
// field is omitted entirely rather than printed as a meaningless
// "unknown" for a credential that was never in question.
func statusCredentialType(method authMethod) auth.CredentialType {
	if !isSecretMethod(method) {
		return ""
	}
	return auth.CredentialTypeUnknown
}

// authMethodLabel renders the parenthetical credential hint for the
// "Logged in" line. Empty for the legacy/unknown case so the line is
// unchanged when no method was recorded. Never says "PAT": the source
// alone cannot tell a Personal Access Token from an Account API Key, so
// the hint stays generic until a trusted response names the type.
func authMethodLabel(v any) string {
	switch v {
	case string(authEnvKey):
		return fmt.Sprintf(" (API credential via %s)", auth.APIKeyEnvVar)
	case string(authStoredKey):
		return " (API credential)"
	case string(authFirebase):
		return ""
	default:
		return ""
	}
}
