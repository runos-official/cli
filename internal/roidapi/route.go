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

// GatewayURL returns where the routes that roid serves only behind the RunOS
// API gateway (rapi) are reached: the API URL, which RUNOS_API_URL overrides.
// On a host split (prod) the issuer host answers 404 not_on_this_host for
// those routes, so the Kubernetes token call goes here and not to BaseURL.
// With no API URL known there is one host, the issuer, as in a deployment
// that names no front service.
func GatewayURL(cfg *config.Config) (string, error) {
	base, err := BaseURL(cfg)
	if err != nil {
		return "", err
	}
	if api := strings.TrimRight(cfg.GetAPIURL(), "/"); api != "" {
		return api, nil
	}
	return base, nil
}
