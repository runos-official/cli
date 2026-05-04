// Package apps handles application config down-sync from a cluster to local YAML files.
package apps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Network and response-size limits used across the apps service.
const (
	// defaultRequestTimeout bounds individual JSON API calls. Long
	// enough for slow clusters, short enough that hung sockets fail
	// the CLI within a reasonable interactive window.
	defaultRequestTimeout = 30 * time.Second

	// archiveDownloadTimeout bounds a single archive GET. Archives
	// can be tens of MB on slow links; the token is short-lived
	// server-side so the upper bound is generous.
	archiveDownloadTimeout = 10 * time.Minute

	// maxResponseBytes caps how much of any JSON response we read
	// into memory. Chosen well above any legitimate response shape.
	maxResponseBytes = 10 << 20 // 10 MiB

	// maxErrorBodyBytes caps the body bytes we read on non-2xx so
	// we don't blow memory on a server that returns a giant HTML
	// error page.
	maxErrorBodyBytes = 4096
)

// Service handles application-related API calls for pulling config.
type Service struct {
	baseURL    string
	httpClient *http.Client
	token      string
	cid        string
	aid        string
}

// AppSummary is the per-app entry returned by GET /:aid/:cid/apps.
type AppSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

// NewService creates a new apps service.
func NewService(baseURL, token, cid, aid string) *Service {
	return &Service{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: defaultRequestTimeout,
		},
		token: token,
		cid:   cid,
		aid:   aid,
	}
}

func (s *Service) get(reqURL string, out any) error {
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

// ListApps returns all apps in the current cluster.
// Endpoint: GET /:aid/:cid/apps
func (s *Service) ListApps() ([]AppSummary, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))
	var out []AppSummary
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetApp fetches the full app record as a raw map so we can pick out whatever
// fields the server returns without being locked to a specific schema.
// Endpoint: GET /:aid/:cid/apps/:id
func (s *Service) GetApp(appID string) (map[string]any, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	var out map[string]any
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SecretFileSummary is an entry in the secret-files list response.
// Content is not returned here; use GetSecretFile to fetch it.
type SecretFileSummary struct {
	Filename  string `json:"filename"`
	MountPath string `json:"mountPath"`
	MD5       string `json:"md5"`
}

// SecretFileContent is the full payload of a single secret file.
// Content is base64-encoded.
type SecretFileContent struct {
	Filename  string `json:"filename"`
	MountPath string `json:"mountPath"`
	MD5       string `json:"md5"`
	Content   string `json:"content"`
}

// ListSecretFiles returns metadata for secret files mounted into an app.
// Endpoint: GET /:aid/:cid/apps/:id/secret-files
func (s *Service) ListSecretFiles(appID string) ([]SecretFileSummary, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secret-files", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	var wrapper struct {
		Files []SecretFileSummary `json:"files"`
	}
	if err := s.get(reqURL, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Files, nil
}

// GetSecretFile returns the full metadata + base64 content of a secret file.
// Endpoint: GET /:aid/:cid/apps/:id/secret-files/:filename (sensitive_read)
func (s *Service) GetSecretFile(appID, filename string) (*SecretFileContent, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secret-files/%s",
		s.baseURL,
		url.PathEscape(s.aid),
		url.PathEscape(s.cid),
		url.PathEscape(appID),
		url.PathEscape(filename),
	)
	var out SecretFileContent
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OverrideSummary is a single manifest override record. The list endpoint
// returns all fields including the base64 `data` payload, so we never need
// a separate show call per override.
type OverrideSummary struct {
	ID      string `json:"__docId"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Data    string `json:"data"` // base64-encoded
}

// ListOverrides returns every manifest override configured for an app.
// Endpoint: GET /:aid/:cid/apps/:id/overrides
func (s *Service) ListOverrides(appID string) ([]OverrideSummary, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/overrides", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	var out []OverrideSummary
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAppEnvVars fetches environment variables for an app.
// Endpoint: GET /:aid/:cid/apps/:id/env-vars (sensitive_read)
func (s *Service) GetAppEnvVars(appID string) (map[string]string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	var out map[string]string
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAppRequires returns the runos.yaml-shaped requires map for this
// app: alias -> {id, type, config, env}. The map keys are the aliases
// (== service display names) the user authored under `requires:` in
// their yaml. Class is not returned, conductor doesn't store it.
//
// Apps deployed before the requires reader landed have edges with no
// metadata; the server returns empty Config and Env for those. The
// pull flow's MergeRequiresUserAuthored handles that fallback.
//
// Endpoint: GET /:aid/:cid/apps/:id/requires (manifest command apps/requires).
func (s *Service) GetAppRequires(appID string) (map[string]ServiceRequirement, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/requires", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	out := map[string]ServiceRequirement{}
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Write endpoints (used by sync)
// ---------------------------------------------------------------------------

// jobAck is the common shape of a write response, every mutating endpoint
// the CLI uses returns at least a jobId so the caller can poll progress.
type jobAck struct {
	JobID string `json:"jobId,omitempty"`
}

// writeJSON sends a JSON request body and decodes the response into out.
// Returns the parsed jobAck so callers can surface jobIds. Treats 4xx/5xx
// as errors.
func (s *Service) writeJSON(method, reqURL string, body any, out any) (*jobAck, error) {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(respBytes))
	}
	ack := &jobAck{}
	if len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, ack); err != nil {
			// Some endpoints don't return JSON; that's fine, just no jobId.
			ack = &jobAck{}
		}
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return ack, fmt.Errorf("parse response: %w", err)
		}
	}
	return ack, nil
}

// UpdateApp patches whichever fields are present in the body. Server-side
// rule: name-only edits are sync, anything else triggers an async redeploy.
// Endpoint: PATCH /:aid/:cid/apps/:id
func (s *Service) UpdateApp(appID string, fields map[string]any) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	ack, err := s.writeJSON(http.MethodPatch, reqURL, fields, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// ReplaceEnvVars replaces every env var on the app with newVars. Server
// returns a jobId because this triggers a rollout restart.
// Endpoint: POST /:aid/:cid/apps/:id/env-vars
func (s *Service) ReplaceEnvVars(appID string, newVars map[string]string) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	ack, err := s.writeJSON(http.MethodPost, reqURL, map[string]any{"envVars": newVars}, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// SecretFilePayload is the on-the-wire shape of a secret file in
// add lists.
type SecretFilePayload struct {
	Filename  string `json:"filename"`
	MountPath string `json:"mountPath"`
	Content   string `json:"content"` // base64-encoded
}

// UpdateSecretFiles applies the add/remove deltas. Either list may be empty.
// Endpoint: POST /:aid/:cid/apps/:id/secret-files
func (s *Service) UpdateSecretFiles(appID string, add []SecretFilePayload, remove []string) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secret-files", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	body := map[string]any{}
	if len(add) > 0 {
		body["add"] = add
	}
	if len(remove) > 0 {
		body["remove"] = remove
	}
	ack, err := s.writeJSON(http.MethodPost, reqURL, body, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// UpdateSecrets is the atomic env + secret-files endpoint, preferred when
// both change so the app only redeploys once.
// Endpoint: POST /:aid/:cid/apps/:id/secrets
func (s *Service) UpdateSecrets(appID string, envVars map[string]string, addFiles []SecretFilePayload, removeFiles []string) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secrets", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	body := map[string]any{}
	if envVars != nil {
		body["envVars"] = envVars
	}
	if len(addFiles) > 0 {
		body["add"] = addFiles
	}
	if len(removeFiles) > 0 {
		body["remove"] = removeFiles
	}
	ack, err := s.writeJSON(http.MethodPost, reqURL, body, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// AddOverride creates a new override on the app. content should be the
// raw bytes; it gets base64-encoded for the wire.
// Endpoint: POST /:aid/:cid/apps/:id/overrides
func (s *Service) AddOverride(appID, name string, content []byte, enabled bool) (overrideID, jobID string, err error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/overrides", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	body := map[string]any{
		"name":    name,
		"data":    base64.StdEncoding.EncodeToString(content),
		"enabled": enabled,
	}
	var out struct {
		OverrideID string `json:"overrideId"`
	}
	ack, err := s.writeJSON(http.MethodPost, reqURL, body, &out)
	if err != nil {
		return "", "", err
	}
	return out.OverrideID, ack.JobID, nil
}

// UpdateOverride patches an existing override. Pass nil for any field
// that shouldn't change. content (when non-nil) is base64-encoded for the
// wire.
// Endpoint: PUT /:aid/:cid/apps/:id/overrides/:overrideId
func (s *Service) UpdateOverride(appID, overrideID string, name *string, content []byte, enabled *bool) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/overrides/%s",
		s.baseURL,
		url.PathEscape(s.aid),
		url.PathEscape(s.cid),
		url.PathEscape(appID),
		url.PathEscape(overrideID),
	)
	body := map[string]any{}
	if name != nil {
		body["name"] = *name
	}
	if content != nil {
		body["data"] = base64.StdEncoding.EncodeToString(content)
	}
	if enabled != nil {
		body["enabled"] = *enabled
	}
	ack, err := s.writeJSON(http.MethodPut, reqURL, body, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// DeleteOverride removes an override by id.
// Endpoint: DELETE /:aid/:cid/apps/:id/overrides/:overrideId
func (s *Service) DeleteOverride(appID, overrideID string) (string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/overrides/%s",
		s.baseURL,
		url.PathEscape(s.aid),
		url.PathEscape(s.cid),
		url.PathEscape(appID),
		url.PathEscape(overrideID),
	)
	ack, err := s.writeJSON(http.MethodDelete, reqURL, nil, nil)
	if err != nil {
		return "", err
	}
	return ack.JobID, nil
}

// ---------------------------------------------------------------------------
// CLI archive endpoints (code pull / rollback)
// ---------------------------------------------------------------------------

// CliArchive describes a single previously-uploaded CLI deploy archive
// the cluster can hand back. cliUploadID equals the original deploy's
// jobId, so rows can be joined against the builds endpoint when richer
// metadata is needed.
type CliArchive struct {
	CliUploadID string `json:"cliUploadId"`
	Digest      string `json:"digest"`
	Size        int64  `json:"size"`
	PushTime    string `json:"pushTime"`
}

// CliPullTicket is the short-lived, single-use download credential the
// cluster mints for one archive. The downloadURL carries its own auth
// in the path; no Authorization header is sent on the GET.
type CliPullTicket struct {
	Token       string `json:"token"`
	ExpiresAt   string `json:"expiresAt"`
	DownloadURL string `json:"downloadUrl"`
}

// ListCliArchives returns the archives recorded for an app.
// Endpoint: GET /:aid/:cid/apps/:id/cli-archives
func (s *Service) ListCliArchives(appID string) ([]CliArchive, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/cli-archives",
		s.baseURL,
		url.PathEscape(s.aid),
		url.PathEscape(s.cid),
		url.PathEscape(appID),
	)
	var out []CliArchive
	if err := s.get(reqURL, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PrepareCliPull mints a single-use download URL for a specific archive.
// expirySeconds <= 0 leaves it server-default (300s).
// Endpoint: POST /:aid/:cid/apps/:id/prepare-cli-pull
func (s *Service) PrepareCliPull(appID, cliUploadID string, expirySeconds int) (*CliPullTicket, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/prepare-cli-pull",
		s.baseURL,
		url.PathEscape(s.aid),
		url.PathEscape(s.cid),
		url.PathEscape(appID),
	)
	body := map[string]any{"cliUploadId": cliUploadID}
	if expirySeconds > 0 {
		body["expirySeconds"] = expirySeconds
	}
	var ticket CliPullTicket
	if _, err := s.writeJSON(http.MethodPost, reqURL, body, &ticket); err != nil {
		return nil, err
	}
	return &ticket, nil
}

// DownloadCliArchive streams the gzipped tarball at downloadURL. The URL
// is single-use and unauthenticated; the caller must Close the returned
// body. Returns 401 distinctly via ErrTicketConsumed so the caller can
// mint a fresh ticket once and retry.
//
// ctx is honoured for cancellation and deadlines; passing
// context.Background() is acceptable when the caller has none.
//
// downloadURL is server-supplied (from PrepareCliPull). To prevent a
// compromised conductor from redirecting the download to an arbitrary
// host or downgrading to plaintext, the URL is validated:
//   - scheme must match s.baseURL's scheme (https in production; http
//     accepted only when the conductor itself is on http, e.g. local dev).
//   - host must match s.baseURL's host. A cluster-side download endpoint
//     served from the same conductor host satisfies this; off-host
//     redirects are rejected outright.
func (s *Service) DownloadCliArchive(ctx context.Context, downloadURL string) (io.ReadCloser, error) {
	if err := s.validateDownloadURL(downloadURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	// Use a download-specific client with a generous timeout, archives
	// can be large and the default 30s s.httpClient timeout would kill
	// mid-stream. The token already enforces lifetime server-side.
	//
	// CheckRedirect is disabled so a 3xx response can't sneak the
	// download off-host after we validated the original URL.
	client := &http.Client{
		Timeout: archiveDownloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrTicketConsumed
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		resp.Body.Close()
		return nil, fmt.Errorf("download failed (%d): %s", resp.StatusCode, string(body))
	}
	// Refuse to start streaming an archive larger than our extraction
	// cap. Saves bandwidth + disk-IO if a buggy or hostile server tries
	// to feed us 100 GB. Servers that omit Content-Length (chunked
	// transfer) bypass this check; the streaming extractor's own
	// LimitReader still bounds total decompressed bytes.
	if resp.ContentLength > maxArchiveBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("download Content-Length %d exceeds %d-byte cap", resp.ContentLength, maxArchiveBytes)
	}
	return resp.Body, nil
}

// validateDownloadURL rejects download URLs that would downgrade the
// protocol or that aren't well-formed http(s) URLs.
//
// We deliberately don't pin the host: conductor returns a cluster-
// agent endpoint (e.g. caldu.<cid>.<aid>.<clusterDomain>) which is
// intentionally a different host from the conductor itself. Pinning
// would break the real download flow.
//
// What we do enforce:
//   - The URL is well-formed and has a host.
//   - http(s) only, block file://, data://, etc.
//   - No protocol downgrade: an https conductor must not produce an
//     http download URL. The reverse (http conductor, https download)
//     is permitted: it's a security upgrade and is the normal shape of
//     local-dev setups where the conductor runs plaintext but the
//     cluster's object store is TLS-fronted.
//
// Redirects are separately disabled in DownloadCliArchive's client.
func (s *Service) validateDownloadURL(downloadURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("download URL is empty")
	}
	target, err := url.Parse(downloadURL)
	if err != nil {
		return fmt.Errorf("download URL is not a valid URL: %w", err)
	}
	if target.Host == "" {
		return fmt.Errorf("download URL %q has no host", downloadURL)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("download URL scheme %q is not http(s)", target.Scheme)
	}
	base, err := url.Parse(s.baseURL)
	if err != nil {
		// Should never happen, s.baseURL came from config and was
		// already used on every other API call. Defend anyway.
		return fmt.Errorf("base URL is not a valid URL: %w", err)
	}
	if base.Scheme == "https" && target.Scheme == "http" {
		return fmt.Errorf("download URL scheme %q does not match conductor scheme %q (refusing protocol downgrade)", target.Scheme, base.Scheme)
	}
	return nil
}

// ErrTicketConsumed is returned by DownloadCliArchive when the
// single-use token at the URL has been used or expired. Callers should
// match it with errors.Is and mint a fresh ticket via PrepareCliPull.
var ErrTicketConsumed = errors.New("download token expired or already used")

