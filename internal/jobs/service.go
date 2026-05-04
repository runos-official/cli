// Package jobs provides job status polling and real-time progress display.
package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
)

// Service handles job-related API calls to fetch job statuses and work items.
type Service struct {
	baseURL    string
	httpClient *http.Client
	token      string
}

// NewService creates a new Service with authentication from the current config.
func NewService() (*Service, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	token, err := getAuthToken(cfg)
	if err != nil {
		return nil, fmt.Errorf("authentication required: run 'runos login' first")
	}

	return &Service{
		baseURL: cfg.GetAPIURL(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		token: token,
	}, nil
}

func getAuthToken(cfg *config.Config) (string, error) {
	if cfg.Firebase == nil {
		return "", fmt.Errorf("not authenticated")
	}
	return auth.GetIDToken(cfg.RefreshToken, cfg.Firebase.APIKey)
}

// GetStatus fetches the current status of a job by its ID.
func (s *Service) GetStatus(jobID string) (*JobStatus, error) {
	reqURL := fmt.Sprintf("%s/jobs/%s", s.baseURL, url.PathEscape(jobID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
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

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var job JobStatus
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &job, nil
}

// GetWorkItems fetches the work items for a job by its ID.
func (s *Service) GetWorkItems(jobID string) (*WorkItemsResponse, error) {
	reqURL := fmt.Sprintf("%s/jobs/%s/workitems", s.baseURL, url.PathEscape(jobID))

	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
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

	var items WorkItemsResponse
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &items, nil
}
