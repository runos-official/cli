// Package auth handles Firebase authentication including token exchange and refresh.
package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	firebaseAuthURL  = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken"
	firebaseTokenURL = "https://securetoken.googleapis.com/v1/token"
)

type signInRequest struct {
	Token             string `json:"token"`
	ReturnSecureToken bool   `json:"returnSecureToken"`
}

// SignInResponse holds the tokens returned after signing in with a custom token.
type SignInResponse struct {
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    string `json:"expiresIn"`
}

type firebaseError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ExchangeCustomToken exchanges a Firebase custom token for ID and refresh tokens.
func ExchangeCustomToken(customToken, apiKey string) (*SignInResponse, error) {
	reqBody := signInRequest{
		Token:             customToken,
		ReturnSecureToken: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	reqURL := fmt.Sprintf("%s?key=%s", firebaseAuthURL, url.QueryEscape(apiKey))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(reqURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var fbErr firebaseError
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&fbErr); err == nil && fbErr.Error.Message != "" {
			return nil, fmt.Errorf("firebase auth failed: %s", fbErr.Error.Message)
		}
		return nil, fmt.Errorf("firebase auth failed with status: %d", resp.StatusCode)
	}

	var result SignInResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// RefreshResponse holds the tokens returned after refreshing an expired ID token.
type RefreshResponse struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    string `json:"expires_in"`
}

// RefreshIDToken uses a refresh token to obtain a new Firebase ID token.
func RefreshIDToken(refreshToken, apiKey string) (*RefreshResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)

	reqURL := fmt.Sprintf("%s?key=%s", firebaseTokenURL, url.QueryEscape(apiKey))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(reqURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var fbErr firebaseError
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&fbErr); err == nil && fbErr.Error.Message != "" {
			return nil, fmt.Errorf("token refresh failed: %s", fbErr.Error.Message)
		}
		return nil, fmt.Errorf("token refresh failed with status: %d", resp.StatusCode)
	}

	var result RefreshResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1*1024*1024)).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetIDToken is a convenience function that refreshes and returns a Firebase ID token.
// Returns an error if the refresh token or API key is empty, or if the refresh fails.
func GetIDToken(refreshToken, apiKey string) (string, error) {
	if refreshToken == "" || apiKey == "" {
		return "", fmt.Errorf("not authenticated")
	}

	resp, err := RefreshIDToken(refreshToken, apiKey)
	if err != nil {
		return "", err
	}

	return resp.IDToken, nil
}
