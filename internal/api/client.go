package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type FirebaseConfig struct {
	APIKey     string `json:"apiKey"`
	AuthDomain string `json:"authDomain"`
	ProjectID  string `json:"projectId"`
}

type InitiateDeviceAuthResponse struct {
	DeviceID  string `json:"deviceId"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

type PollDeviceAuthRequest struct {
	DeviceID string `json:"deviceId"`
	Token    string `json:"token"`
}

type PollDeviceAuthResponse struct {
	Success     bool            `json:"success"`
	Error       string          `json:"error,omitempty"`
	Message     string          `json:"message,omitempty"`
	CustomToken string          `json:"customToken,omitempty"`
	AccountID   string          `json:"accountId,omitempty"`
	Firebase    *FirebaseConfig `json:"firebase,omitempty"`
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
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

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

	var result PollDeviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
