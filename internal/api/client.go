// Package api provides the HTTP client for the Conductor API.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for communicating with the Conductor API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new API client configured with the given base URL.
func NewClient(baseURL string) *Client {
	return NewClientWithTimeout(baseURL, 10*time.Second)
}

// NewClientWithTimeout creates a client whose calls carry the given
// deadline. Used by a probe made only to EXPLAIN a failure the user
// already has, which must give up quickly rather than add its own wait
// to a command that has already failed.
func NewClientWithTimeout(baseURL string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// FirebaseConfig holds Firebase project settings returned by the device auth flow.
type FirebaseConfig struct {
	APIKey     string `json:"apiKey"`
	AuthDomain string `json:"authDomain"`
	ProjectID  string `json:"projectId"`
}

// InitiateDeviceAuthResponse represents the response from initiating a device auth session.
type InitiateDeviceAuthResponse struct {
	DeviceID  string `json:"deviceId"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// PollDeviceAuthRequest is the request body for polling a device auth session.
type PollDeviceAuthRequest struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

// PollDeviceAuthResponse represents the response from polling a device auth session.
type PollDeviceAuthResponse struct {
	Success     bool            `json:"success"`
	Error       string          `json:"error,omitempty"`
	Message     string          `json:"message,omitempty"`
	CustomToken string          `json:"customToken,omitempty"`
	AccountID   string          `json:"accountId,omitempty"`
	Firebase    *FirebaseConfig `json:"firebase,omitempty"`
}

// InitiateDeviceAuth starts a new device authorization flow and returns the device ID and token.

// BaseURL returns the client's base URL, for the few callers that must build a request the
// generic Do helper cannot express (a conditional GET carrying If-None-Match). Prefer Do.
func (c *Client) BaseURL() string {
	return strings.TrimRight(c.baseURL, "/")
}

// HTTP exposes the underlying client so a caller building its own *http.Request (again, only for
// headers Do does not set) shares this client's timeout and transport.
func (c *Client) HTTP() *http.Client {
	return c.httpClient
}

func (c *Client) InitiateDeviceAuth() (*InitiateDeviceAuthResponse, error) {
	url := fmt.Sprintf("%s/auth/device/initiate", c.baseURL)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("initiate failed with status: %d", resp.StatusCode)
	}

	var result InitiateDeviceAuthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// PollDeviceAuth checks whether the user has completed the device authorization flow.
func (c *Client) PollDeviceAuth(deviceID, token string) (*PollDeviceAuthResponse, error) {
	reqBody := PollDeviceAuthRequest{
		DeviceID: deviceID,
		Token:    token,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/auth/device/poll", c.baseURL)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll failed with status: %d", resp.StatusCode)
	}

	var result PollDeviceAuthResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
