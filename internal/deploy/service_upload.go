package deploy

import (
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

// ValidatePreparedTarget refuses a preparation response that does not
// match the target this request named, that is missing a field
// handbook 992 guarantees, or that is already past its own expiry,
// BEFORE any local state is written or the archive is touched (FPL441
// "Immutable target" / "CLI upload"; story 346, findings 1328, 1354
// and 1391). wantAppID is the app id the CLI already knows locally
// (deployConfig.ID); empty means this was a new-app request, which
// has no prior app to compare against (osid alone identifies a
// brand-new app) but still requires every other published field.
//
// Finding 1391: the first cut of this function accepted a MISSING
// appId or expiry as silently fine, when the published contract
// guarantees both on every accepted request; only a genuine mismatch
// or a parseable-but-past expiry refused. Missing required fields now
// refuse exactly like a mismatched one.
func ValidatePreparedTarget(wantAppID string, resp *PrepareResponse) error {
	if resp == nil {
		return fmt.Errorf("preparation returned no response")
	}
	if wantAppID != "" {
		if resp.AppID == "" {
			return fmt.Errorf("preparation response did not name an app; this deploy requested app %q", wantAppID)
		}
		if resp.AppID != wantAppID {
			return fmt.Errorf("preparation response named app %q, this deploy requested app %q", resp.AppID, wantAppID)
		}
	} else if resp.AppID == "" && resp.OSID == "" {
		return fmt.Errorf("preparation response named neither an app id nor an osid")
	}
	if resp.JobID == "" {
		return fmt.Errorf("preparation response did not name a job")
	}
	if resp.UploadURL == "" {
		return fmt.Errorf("preparation response did not name an upload destination")
	}
	if resp.Token == "" {
		return fmt.Errorf("preparation response did not name an upload token")
	}
	if resp.ExpiresAt == "" {
		return fmt.Errorf("preparation response did not name an expiry")
	}
	expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
	if err != nil {
		return fmt.Errorf("preparation response has a malformed expiry %q: %w", resp.ExpiresAt, err)
	}
	if !time.Now().Before(expiresAt) {
		return fmt.Errorf("preparation response already expired at %s", resp.ExpiresAt)
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
