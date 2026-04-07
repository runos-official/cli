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

// PrepareResponse is the response from the prepareCliDeployment endpoint
type PrepareResponse struct {
	JobID     string               `json:"jobId"`
	OSID      string               `json:"osid"`
	AppID     string               `json:"appId"`
	UploadURL string               `json:"uploadUrl"`
	Token     string               `json:"token"`
	ExpiresAt string               `json:"expiresAt"`
	Services  []ProvisionedService `json:"services,omitempty"`
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

// PrepareDeployment calls the prepareCliDeployment endpoint to get an upload token
func (s *Service) PrepareDeployment(config *DeployConfig) (*PrepareResponse, error) {
	// Endpoint: /:aid/:cid/prepareCliDeployment (from manifest)
	reqURL := fmt.Sprintf("%s/%s/%s/prepareCliDeployment", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid))

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

// GetAppEnvVars fetches environment variables for an application
func (s *Service) GetAppEnvVars(appID string) (map[string]string, error) {
	// Endpoint: /:aid/:cid/apps/:id/envs
	reqURL := fmt.Sprintf("%s/%s/%s/apps/%s/envs", s.baseURL, url.PathEscape(s.aid), url.PathEscape(s.cid), url.PathEscape(appID))

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
