package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/runos-official/cli/internal/config"
)

// ErrNotAuthenticated is returned when no credential path is available:
// no RUNOS_API_KEY, no stored PAT, and no Firebase refresh-token config.
// Callers use errors.Is to distinguish the expected "not signed in yet"
// state (new install, logged out) from genuine failures, so first-run
// output can stay friendly instead of dumping recovery jargon.
var ErrNotAuthenticated = errors.New("not authenticated")

// APIKeyEnvVar is the env var the CLI reads to use a RunOS account API
// key (PAT) instead of the interactive Firebase refresh-token flow.
// Designed for CI/CD: set RUNOS_API_KEY in the runner's secret store
// and the CLI authenticates against conductor without any local config.
const APIKeyEnvVar = "RUNOS_API_KEY"

// AccountIDEnvVar is the env var the CLI reads to address an account
// without a local config (CI/CD shape, paired with RUNOS_API_KEY).
const AccountIDEnvVar = "RUNOS_ACCOUNT_ID"

// ValidateAuthEnvVars refuses early when RUNOS_API_KEY or
// RUNOS_ACCOUNT_ID is *explicitly* set to empty (`export FOO=` rather
// than leaving it unset). I25-G / I25-J: pre-fix, set-but-empty fell
// through to the cached Firebase token / config.json AccountID
// silently, so a CI runner that intended to use a PAT but typo'd the
// secret-store reference (`$VAR_THAT_DOES_NOT_EXIST` expanded to empty)
// got unexpected success using the developer's stored credentials.
// Distinguishes "unset" (fine; falls back to Firebase) from
// "explicitly empty" (refuse, since intent was clearly a PAT path).
func ValidateAuthEnvVars(lookup func(string) (string, bool)) error {
	for _, name := range []string{APIKeyEnvVar, AccountIDEnvVar} {
		v, ok := lookup(name)
		if ok && strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is set but empty; either unset it to fall back to interactive auth, or set it to a real value", name)
		}
	}
	return nil
}

// ResolveToken returns the bearer token to send in Authorization headers.
// Three paths, in priority order:
//
//  1. If RUNOS_API_KEY is set in the environment, return it verbatim.
//     This is the CI/CD path: a PAT minted via account/api-keys/add
//     authenticates directly, no Firebase round-trip, no on-disk
//     credentials. cfg may be nil or partially populated in this mode.
//
//  2. If a PAT is persisted on disk (cfg.APIKey, set by
//     `runos login --api-key`), return it. This is the developer who
//     forced a PAT instead of the browser flow. The env var still wins
//     over the stored key so a CI runner can override a local default.
//
//  3. Otherwise, fall back to the Firebase refresh-token exchange the
//     interactive `runos login` flow set up. This is the human-developer
//     path: refresh token + Firebase API key live in ~/.runos/config.json
//     and produce a fresh ID token (~1h lifetime) per call.
//
// Returns a clear "not authenticated" error when no path is available
// (no env var, no stored PAT, AND no Firebase config).
func ResolveToken(cfg *config.Config) (string, error) {
	// Issue 110: a PAT pasted from Slack / docs / web UIs often carries
	// a trailing newline or leading space. Pre-fix the trailing-newline
	// case leaked "net/http: invalid header field value for Authorization"
	// because net/http refuses CR/LF in header values; the leading-space
	// case silently passed (asymmetric). TrimSpace canonicalises both so
	// the user sees the same clean result either way. The same trim
	// covers a stored PAT for parity.
	if pat := strings.TrimSpace(os.Getenv(APIKeyEnvVar)); pat != "" {
		return pat, nil
	}
	if cfg != nil {
		if pat := strings.TrimSpace(cfg.APIKey); pat != "" {
			return pat, nil
		}
	}
	if cfg == nil || cfg.Firebase == nil {
		return "", fmt.Errorf("%w: run 'runos login' (or 'runos login --api-key <pat>') or set %s", ErrNotAuthenticated, APIKeyEnvVar)
	}
	return GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

// HasCredentials reports whether any auth path is available WITHOUT a
// network round-trip: a PAT in the env, a stored PAT, or Firebase
// refresh-token credentials on disk. Used for cheap "is the user signed
// in?" checks (welcome banner, first-run guidance) that must not trigger
// a token refresh. A true result means credentials are present, not that
// they are still valid.
func HasCredentials(cfg *config.Config) bool {
	if UsingAPIKey() {
		return true
	}
	if cfg == nil {
		return false
	}
	if strings.TrimSpace(cfg.APIKey) != "" {
		return true
	}
	return cfg.Firebase != nil && strings.TrimSpace(cfg.RefreshToken) != ""
}

// UsingAPIKey reports whether the current process is configured to use
// a PAT (RUNOS_API_KEY env var). Callers that need to skip Firebase-
// specific setup (e.g. config-required gates that don't apply when a
// PAT is the credential) check this rather than re-reading the env.
// Mirrors ResolveToken's TrimSpace so a whitespace-only PAT (issue 110)
// doesn't trip the "using a PAT" predicate.
func UsingAPIKey() bool {
	return strings.TrimSpace(os.Getenv(APIKeyEnvVar)) != ""
}
