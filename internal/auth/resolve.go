package auth

import (
	"fmt"
	"os"

	"github.com/runos-official/cli/internal/config"
)

// APIKeyEnvVar is the env var the CLI reads to use a RunOS account API
// key (PAT) instead of the interactive Firebase refresh-token flow.
// Designed for CI/CD: set RUNOS_API_KEY in the runner's secret store
// and the CLI authenticates against conductor without any local config.
const APIKeyEnvVar = "RUNOS_API_KEY"

// ResolveToken returns the bearer token to send in Authorization headers.
// Two paths, in priority order:
//
//  1. If RUNOS_API_KEY is set in the environment, return it verbatim.
//     This is the CI/CD path: a PAT minted via account/api-keys/add
//     authenticates directly, no Firebase round-trip, no on-disk
//     credentials. cfg may be nil or partially populated in this mode.
//
//  2. Otherwise, fall back to the Firebase refresh-token exchange the
//     interactive `runos login` flow set up. This is the human-developer
//     path: refresh token + Firebase API key live in ~/.runos/config.json
//     and produce a fresh ID token (~1h lifetime) per call.
//
// Returns a clear "not authenticated" error when neither path is
// available (no env var AND no Firebase config).
func ResolveToken(cfg *config.Config) (string, error) {
	if pat := os.Getenv(APIKeyEnvVar); pat != "" {
		return pat, nil
	}
	if cfg == nil || cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated: run 'runos login' or set %s", APIKeyEnvVar)
	}
	return GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

// UsingAPIKey reports whether the current process is configured to use
// a PAT (RUNOS_API_KEY env var). Callers that need to skip Firebase-
// specific setup (e.g. config-required gates that don't apply when a
// PAT is the credential) check this rather than re-reading the env.
func UsingAPIKey() bool {
	return os.Getenv(APIKeyEnvVar) != ""
}
