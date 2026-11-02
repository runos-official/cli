// Package roidapi sends the CLI's calls for the RunOS identity service's own
// API (roles, groups, grants and effective access) straight to the identity
// service, never through conductor. Router (router.go) decides which of the two
// RunOS hosts answers a call, so a later public API front changes one place.
package roidapi

import (
	"errors"
	"strings"

	"github.com/runos-official/cli/internal/config"
)

// ErrNotRoid means the environment does not sign in through the RunOS
// identity service, so it has no identity API to call.
var ErrNotRoid = errors.New("this environment does not use the RunOS identity service, so access commands are not available here")

// BaseURL returns the issuer the CLI signs in with. It is the second host of the Router and the
// only host of a deployment that names no front service.
func BaseURL(cfg *config.Config) (string, error) {
	if cfg == nil || cfg.AuthIssuer == "" {
		return "", ErrNotRoid
	}
	return strings.TrimRight(cfg.AuthIssuer, "/"), nil
}

// GatewayURL returns where the routes that roid serves only behind the RunOS
// API gateway (rapi) are reached: the API URL, which RUNOS_API_URL overrides.
// On a host split (prod) the issuer host answers 404 not_on_this_host for
// those routes, so they go here first. On dev this host is conductor and answers
// 404, which is why a Router retries on BaseURL after a 404.
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
