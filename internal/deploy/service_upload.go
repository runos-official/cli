package deploy

import (
	"fmt"
	"net/url"
	"strings"
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
