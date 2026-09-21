package dynacmd

import (
	"fmt"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

// ExecuteWithInput drives a manifest command without going through cobra
// flag parsing. Used by static commands (e.g. services_pull / services_diff
// / services_sync) that already have their input as a typed map. Returns
// the raw response body on 2xx; on non-2xx, returns an *APIError that
// carries the status code and the raw body so callers can format it (e.g.
// 409 dependents list out of services delete).
//
// positionalArgs feeds the same buildEndpoint path that Execute uses, so
// fields marked positional in the manifest are substituted into the URL.
// input contains every non-positional value the command needs (PATCH/POST
// body fields, GET/DELETE query parameters); the dispatch path filters out
// keys that double as path parameters.
//
// cid empty falls back to the default cluster id from config, matching
// Execute's "no --cid means use default" behaviour.
func (e *Executor) ExecuteWithInput(cmdDef manifest.Command, positionalArgs []string, input map[string]any, cid string) ([]byte, error) {
	return e.executeWithInput(cmdDef, positionalArgs, input, cid, requestOptions{})
}

// ExecuteWithJSONInput sends a JSON object for a manifest POST, including an empty object.
// Service sync uses it when a create plan has no optional fields.
func (e *Executor) ExecuteWithJSONInput(cmdDef manifest.Command, positionalArgs []string, input map[string]any, cid string) ([]byte, error) {
	return e.executeWithInput(cmdDef, positionalArgs, input, cid, requestOptions{includeEmptyJSONObject: true})
}

func (e *Executor) executeWithInput(cmdDef manifest.Command, positionalArgs []string, input map[string]any, cid string, options requestOptions) ([]byte, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	token, err := e.getAuthToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' first (%w)", err)
	}
	if cid == "" {
		cid = cfg.GetDefaultClusterID()
	}
	return e.dispatchWithOptions(cmdDef, positionalArgs, input, cid, cfg, token, options)
}
