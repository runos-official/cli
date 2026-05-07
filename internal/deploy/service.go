// Package deploy handles application deployment including archive creation and upload.
package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Service handles deployment-related API calls
type Service struct {
	baseURL    string
	httpClient *http.Client
	token      string
	cid        string
	aid        string
}

// PrepareResponse is the response from the prepare-cli-deployment endpoint.
//
// CliUploadID is the same identifier that GET /:aid/:cid/apps/:id/cli-archives
// uses to key archives, and is what `prepare-cli-pull` accepts. The CLI
// records it in the per-app sidecar (.runos-source-version) right after a
// successful upload, so future deploys can detect upstream archives that
// have appeared since this one. JobID is the orchestrating deploy job (used
// by --follow polling); the two ids are conceptually different even when
// they happen to share a value.
type PrepareResponse struct {
	JobID       string               `json:"jobId"`
	OSID        string               `json:"osid"`
	AppID       string               `json:"appId"`
	UploadURL   string               `json:"uploadUrl"`
	Token       string               `json:"token"`
	ExpiresAt   string               `json:"expiresAt"`
	CliUploadID string               `json:"cliUploadId,omitempty"`
	Services    []ProvisionedService `json:"services,omitempty"`
}

// ProvisionedService represents a service that will be provisioned for an app dependency
type ProvisionedService struct {
	Alias string `json:"alias"`
	ID    string `json:"id"`
	Type  string `json:"type"`
	IsNew bool   `json:"isNew"`
}

// NewService creates a new deployment service
func NewService(baseURL, token, cid, aid string) *Service {
	return &Service{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		token: token,
		cid:   cid,
		aid:   aid,
	}
}

// PrepareDeployment calls the prepare-cli-deployment endpoint to get an upload token
func (s *Service) PrepareDeployment(config *DeployConfig) (*PrepareResponse, error) {
	// Endpoint: /:aid/:cid/prepare-cli-deployment (camelCase alias still works)
	reqURL := fmt.Sprintf("%s/%s/%s/prepare-cli-deployment", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

	jsonBody, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result PrepareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// UploadTarball uploads the gzipped tarball to the cluster agent
func (s *Service) UploadTarball(uploadURL string, uploadToken string, data io.Reader) error {
	// If uploadURL is relative, prepend the base URL
	fullURL := uploadURL
	if len(uploadURL) > 0 && uploadURL[0] == '/' {
		fullURL = s.baseURL + uploadURL
	}

	req, err := http.NewRequest(http.MethodPost, fullURL, data)
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}

	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+uploadToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("failed to read upload response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

// NetworkAccess represents a network access entry for an app
type NetworkAccess struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// GetNetworkAccess fetches network access URLs for an application
func (s *Service) GetNetworkAccess(appID string) ([]NetworkAccess, error) {
	// Endpoint: /:aid/:cid/apps/:id/networkAccess
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/networkAccess", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result []NetworkAccess
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// AppInfo represents an application from the apps list
type AppInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Port int    `json:"port"`
}

// AppShow is the subset of GET /apps/:id the deploy command branches on. The
// real response is much larger; we decode only what runos deploy needs to
// pick between the cli- and vcs-deploy paths.
type AppShow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DeployType string `json:"deployType"`
}

// VCSDeployResponse is what POST /apps/:id/deploy returns (202 + jobId).
type VCSDeployResponse struct {
	JobID string `json:"jobId"`
}

// GetApp fetches the app details for branching on deployType. Returns the
// raw HTTP error so callers can distinguish 404 from other failures.
func (s *Service) GetApp(appID string) (*AppShow, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result AppShow
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// DeployVCS triggers a VCS deploy at the given commit sha. The conductor
// pulls source from the app's linked GitHub/GitLab integration, reconciles
// runos.yaml + runos.service.*.yaml from the committed tree, builds (when
// not already in Harbor), patches the deployment, and watches rollout.
//
// configPath is the repo-relative location of the runos.yaml the cluster
// agent should read on this deploy. Empty string means "use whatever the
// AppDocument has stored" (CI mode without a yaml on disk). Non-empty
// values are persisted to the AppDocument so subsequent deploys inherit
// them — the yaml is the source of truth for its own repo location.
func (s *Service) DeployVCS(appID, sha, configPath string) (*VCSDeployResponse, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/deploy", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	bodyMap := map[string]string{"sha": sha}
	if configPath != "" {
		bodyMap["configPath"] = configPath
	}
	jsonBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result VCSDeployResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// FindAppByName searches for an app by name in the cluster
func (s *Service) FindAppByName(appName string) (*AppInfo, error) {
	// Endpoint: /:aid/:cid/apps
	reqURL := fmt.Sprintf("%s/%s/%s/apps", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var apps []AppInfo
	if err := json.Unmarshal(body, &apps); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	for _, app := range apps {
		if app.Name == appName {
			return &app, nil
		}
	}

	return nil, nil // Not found
}

// AppDependency represents a dependency of an app
type AppDependency struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// fetchEnvVarMap is the shared implementation for GET /env-vars and
// /secret-env-vars: an authenticated GET that decodes the response into a
// flat string map.
func (s *Service) fetchEnvVarMap(reqURL string) (map[string]string, error) {
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var envVars map[string]string
	if err := json.Unmarshal(body, &envVars); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return envVars, nil
}

// GetAppSecretEnvVars fetches the sensitive (Secret-backed) env vars for an
// application. Endpoint: /:aid/:cid/apps/:id/secret-env-vars
func (s *Service) GetAppSecretEnvVars(appID string) (map[string]string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/secret-env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	return s.fetchEnvVarMap(reqURL)
}

// GetAppEnvVars fetches the plain (ConfigMap-backed) env vars for an
// application. Endpoint: /:aid/:cid/apps/:id/env-vars
func (s *Service) GetAppEnvVars(appID string) (map[string]string, error) {
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/env-vars", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))
	return s.fetchEnvVarMap(reqURL)
}

// GetAppDependencies fetches dependencies for an application
func (s *Service) GetAppDependencies(appID string) ([]AppDependency, error) {
	// Endpoint: /:aid/:cid/apps/:id/dependencies
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/dependencies", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var deps []AppDependency
	if err := json.Unmarshal(body, &deps); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return deps, nil
}
