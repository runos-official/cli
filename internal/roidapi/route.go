// Package roidapi sends the CLI's calls for the RunOS identity service's own
// API (roles, groups, grants and effective access) straight to the identity
// service, never through conductor. BaseURL is the one place that decides
// where those calls go, so a later public API front changes one function.
package roidapi

import (
	"errors"
	"strings"

	"github.com/runos-official/cli/internal/config"
)

// ErrNotRoid means the environment does not sign in through the RunOS
// identity service, so it has no identity API to call.
var ErrNotRoid = errors.New("this environment does not use the RunOS identity service, so access commands are not available here")

// BaseURL returns where the identity service's own API is served for this
// environment: today the issuer the CLI signs in with.
func BaseURL(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.AuthIssuer == "" {
		return "", ErrNotRoid
	}
	return strings.TrimRight(cfg.AuthIssuer, "/"), nil
}
