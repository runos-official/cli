// Package harbor implements the client side of the Harbor build-image
// primitive: build an arbitrary, app-less container image from a local
// build context and push it into the cluster's system Harbor under the
// managed runos-apps project.
//
// Transport mirrors the proven CLI-deploy upload model (foreman
// objective 47): the CLI calls a prepare step on conductor, which mints a
// short-lived upload token and a presigned cluster-agent upload URL; the
// CLI then uploads the build-context tarball DIRECTLY to the cluster
// agent (bytes never traverse conductor) and follows the conductor job.
package harbor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/deploy"
)

const (
	// prepareTimeout bounds the prepare round-trip (a small JSON POST).
	prepareTimeout = 60 * time.Second
	// uploadTimeout bounds the tarball upload to the cluster agent.
	// Build contexts for this primitive are meant to be small and
	// self-contained, but the cap is generous so a slow link doesn't
	// truncate a legitimate context.
	uploadTimeout = 10 * time.Minute
	// maxRespBody caps how much of a response body we read into memory
	// for parsing or error reporting.
	maxRespBody = 10 * 1024 * 1024
)

// APIError is returned for non-2xx responses so callers can branch on the
// status code (e.g. surface a conductor 400 body verbatim) instead of
// string-matching the message.
type APIError struct {
	StatusCode int
	Body       []byte
}

// Error renders the conductor body alongside the status code.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (%d): %s", e.StatusCode, string(e.Body))
}

// BuildImageRequest is the prepare-endpoint request body. repo is the
// repository name WITHIN the fixed runos-apps project (no project prefix,
// no :tag). Conductor enforces project=runos-apps and validates the repo
// and tags server-side; the CLI sends repo + tags split so the project
// segment stays owned by the server.
type BuildImageRequest struct {
	Repo         string                    `json:"repo"`
	Tags         []string                  `json:"tags"`
	Dockerfile   string                    `json:"dockerfile,omitempty"`
	BuildArgsCli []deploy.BuildArgCliEntry `json:"buildArgsCli,omitempty"`
}

// PrepareResponse is the 202 envelope from the prepare endpoint. UploadURL
// is the presigned cluster-agent endpoint the tarball is uploaded to;
// Token authorizes that single upload. Images lists the fully-qualified
// refs (runos-apps/<repo>:<tag>) the build will push, for display.
type PrepareResponse struct {
	JobID     string   `json:"jobId"`
	UploadURL string   `json:"uploadUrl"`
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expiresAt"`
	UploadID  string   `json:"uploadId"`
	Images    []string `json:"images"`
}

// Service issues the Harbor build-image prepare + upload calls.
type Service struct {
	baseURL     string
	token       string
	aid         string
	cid         string
	prepareHTTP *http.Client
	uploadHTTP  *http.Client
}

// NewService builds a Service bound to an account + cluster. The upload
// client disables redirects so a server-supplied upload URL cannot bounce
// the bearer token to another host.
func NewService(baseURL, token, aid, cid string) *Service {
	return &Service{
		baseURL: baseURL,
		token:   token,
		aid:     aid,
		cid:     cid,
		prepareHTTP: &http.Client{
			Timeout: prepareTimeout,
		},
		uploadHTTP: &http.Client{
			Timeout: uploadTimeout,
			// Never follow a redirect on the upload: the request carries
			// the upload bearer token in a header, and a 3xx to another
			// host would otherwise leak it. Return the redirect response
			// as-is so the caller sees the non-2xx and fails loudly.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// PrepareBuildImage calls POST /:aid/:cid/services/harbor/build-image and
// returns the upload coordinates + job id. The verb takes no service id:
// the system Harbor is resolved from the cluster server-side.
func (s *Service) PrepareBuildImage(req BuildImageRequest) (*PrepareResponse, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/services/harbor/build-image",
		s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

	jsonBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.prepareHTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: body}
	}

	var result PrepareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// UploadContext uploads the gzipped build-context tarball to the presigned
// cluster-agent upload URL. That upload is what triggers the build. The
// upload URL is server-supplied, so it is scheme-validated (an absolute
// URL must be https) before the bearer token is attached.
func (s *Service) UploadContext(uploadURL, uploadToken string, data io.Reader) error {
	fullURL, err := resolveUploadURL(s.baseURL, uploadURL)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, fullURL, data)
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+uploadToken)

	resp, err := s.uploadHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return fmt.Errorf("failed to read upload response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// resolveUploadURL turns a server-supplied upload URL into the absolute
// URL the tarball is POSTed to, validating it cannot downgrade transport
// or point the bearer token at an untrusted host.
//
//   - A relative URL ("/cli-deploy/<token>") is resolved against the
//     trusted, locally-configured baseURL and inherits its scheme.
//   - An absolute URL must be https. http (or any other scheme) is
//     rejected: it would either transmit the upload token in clear text
//     or hand it to an unexpected protocol handler.
//
// Pure helper so the scheme-validation rule is unit-testable without a
// live server.
func resolveUploadURL(baseURL, uploadURL string) (string, error) {
	uploadURL = strings.TrimSpace(uploadURL)
	if uploadURL == "" {
		return "", fmt.Errorf("server returned an empty upload URL")
	}

	// Relative path: resolve against the trusted configured base.
	if strings.HasPrefix(uploadURL, "/") {
		return strings.TrimRight(baseURL, "/") + uploadURL, nil
	}

	u, err := url.Parse(uploadURL)
	if err != nil {
		return "", fmt.Errorf("server returned an unparseable upload URL %q: %w", uploadURL, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("refusing to upload to non-https URL %q (scheme %q); the upload carries a bearer token", uploadURL, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server returned an upload URL with no host: %q", uploadURL)
	}
	return uploadURL, nil
}
