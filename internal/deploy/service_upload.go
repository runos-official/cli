package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PrepareExistingAppDeployment calls the app-scoped CLI-deployment
// preparation endpoint (story 337, FPL428; handbook article
// conductor-api/apps-management/post-apps-prepare-cli-deployment):
// POST /:aid/:cid/apps/:id/prepare-cli-deployment. Same runos.yaml-shaped
// request body and PrepareResponse shape as the broad, cluster-scoped
// PrepareDeployment, but bound to an EXISTING app in the path: it can
// never create a new app, link a service that isn't already a
// dependency of this one, or provision cluster-wide build credentials.
//
// The CLI calls this route whenever the local config already names an
// app (deployConfig.ID != ""), never the broad route, and never falls
// back from a refusal on this route to the broad one (story 346, D1/D4:
// a scoped denial -- including a 503 while the agent-side stage-recheck
// capability is still closed -- is final). The broad PrepareDeployment
// stays reachable only for the one case this route structurally cannot
// serve: minting a brand-new app.
func (s *Service) PrepareExistingAppDeployment(appID string, config *DeployConfig) (*PrepareResponse, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/prepare-cli-deployment", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	return s.postPrepareDeployment(reqURL, config)
}

// NeedsBroadPreparation reports whether cfg's requires block asks the
// preparation call to do something the app-scoped route (handbook
// article conductor-api/apps-management/post-apps-prepare-cli-deployment)
// structurally cannot: create a new service from a requires.<alias>
// entry that has no existing id yet. That route "never creates a
// service and never links a previously-unrelated one" -- true for
// every credential, not only a scoped one, so this is a request-shape
// decision, never a guess at credential type (story 346, finding
// 1352). The caller uses the broad, cluster-scoped route instead,
// exactly as it already does for a brand-new app.
func (cfg *DeployConfig) NeedsBroadPreparation() bool {
	for _, req := range cfg.Requires {
		if req.ID == "" {
			return true
		}
	}
	return false
}

// ExplainAppScopedPrepareRefusal recognizes the app-scoped
// preparation route's own documented refusal codes (handbook 992) and
// appends an actionable hint naming WHY this narrower route refused
// something the broad route used to allow, instead of leaving the
// caller with only a bare API error (story 346, finding 1352). Any
// other error, or a body this function cannot parse, passes through
// unchanged.
func ExplainAppScopedPrepareRefusal(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	var body struct {
		Code string `json:"code"`
	}
	if jsonErr := json.Unmarshal(apiErr.Body, &body); jsonErr != nil {
		return err
	}
	switch body.Code {
	case "authz.cannot_provision", "authz.not_a_dependency":
		return fmt.Errorf("%w (an app-scoped deploy grant can only redeploy existing linked services; add the service through the console or its own create command first, then re-add its id to runos.yaml's requires block)", err)
	case "authz.cannot_reconfigure":
		return fmt.Errorf("%w (an app-scoped deploy grant cannot resize or reconfigure an existing linked service's class or config)", err)
	case "authz.conflicting_target":
		return fmt.Errorf("%w (the request named a different account, cluster or app than the one this credential is deploying)", err)
	default:
		return err
	}
}

// ValidatePreparedTarget refuses a preparation response that does not
// match the target this request named, or that is already past its
// own expiry, BEFORE any local state is written or the archive is
// touched (FPL441 "Immutable target" / "CLI upload"; story 346,
// findings 1328 and 1354). wantAppID is the app id the CLI already
// knows locally (deployConfig.ID); empty means this was a new-app
// request, which has no prior target to compare against and skips
// that half of the check.
func ValidatePreparedTarget(wantAppID string, resp *PrepareResponse) error {
	if resp == nil {
		return fmt.Errorf("preparation returned no response")
	}
	if wantAppID != "" && resp.AppID != "" && resp.AppID != wantAppID {
		return fmt.Errorf("preparation response named app %q, this deploy requested app %q", resp.AppID, wantAppID)
	}
	if resp.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		if err == nil && !time.Now().Before(expiresAt) {
			return fmt.Errorf("preparation response already expired at %s", resp.ExpiresAt)
		}
	}
	return nil
}

// validateUploadURL rejects an upload URL that would downgrade the
// transport below the conductor's own scheme, or that isn't a
// well-formed http(s) URL. Mirrors apps.Service.validateDownloadURL
// (same finding family, the upload direction: finding 225). The
// upload request carries the single-use upload bearer, so a
// compromised or buggy conductor must not be able to steer it to an
// arbitrary host or a plaintext endpoint.
//
// A root-relative path ("/upload/...") is accepted without further
// checks: UploadTarball resolves it against s.baseURL, so it inherits
// the conductor's own scheme and host by construction.
func validateUploadURL(baseURL, uploadURL string) error {
	if uploadURL == "" {
		return fmt.Errorf("upload URL is empty")
	}
	if strings.HasPrefix(uploadURL, "/") {
		return nil
	}
	target, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("upload URL is not a valid URL: %w", err)
	}
	if target.Host == "" {
		return fmt.Errorf("upload URL %q is neither an absolute URL nor a root-relative path", uploadURL)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("upload URL scheme %q is not http(s)", target.Scheme)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		// Should never happen: baseURL came from config and was already
		// used on every other API call. Defend anyway.
		return fmt.Errorf("base URL is not a valid URL: %w", err)
	}
	if base.Scheme == "https" && target.Scheme == "http" {
		return fmt.Errorf("upload URL scheme %q does not match conductor scheme %q (refusing protocol downgrade)", target.Scheme, base.Scheme)
	}
	return nil
}
