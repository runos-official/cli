// Package auth handles Firebase authentication including token exchange and refresh.
package auth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

/*
Google's two endpoints, as VARIABLES so a test can point them somewhere it controls.

Every path that signs in or refreshes goes through here, which used to make the whole of `vpn up`
and `runos status` untestable without reaching Google: a test would have had to make a real request
with a real credential. The seam matches the one `cmd` already uses for the browser flow
(`authenticateInBrowser`). Nothing outside a test ever writes these.
*/
var (
	firebaseAuthURL  = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken"
	firebaseTokenURL = "https://securetoken.googleapis.com/v1/token"
)

// SetEndpointsForTest points both endpoints at a stub and restores them when the test ends.
// Exported because the `cmd` package's flow tests need it; it has no other caller.
func SetEndpointsForTest(t interface{ Cleanup(func()) }, authURL, tokenURL string) {
	previousAuth, previousToken := firebaseAuthURL, firebaseTokenURL
	firebaseAuthURL, firebaseTokenURL = authURL, tokenURL
	t.Cleanup(func() { firebaseAuthURL, firebaseTokenURL = previousAuth, previousToken })
}

/*
What each call is called in an error, because the reader meets it with no context around it: a
menu-bar line or a `runos status` field, and nothing to say which of the two round trips it was.
*/
const (
	signInCall  = "the sign-in"
	refreshCall = "the token refresh"
)

// maxReplyBytes bounds what is read back. Google's replies are a few hundred bytes; a wifi portal
// answers with a web page, and this path must not pull an unbounded one into memory before
// deciding it was not a token.
const maxReplyBytes = 1 << 20

// readReply reads the whole body once, so the same bytes can be tried as a token, as Google's
// error envelope, and as evidence of an interception. Streaming it into a decoder, which is what
// this used to do, consumes it and leaves nothing to describe when the decode fails.
func readReply(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
}

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
		return nil, transportFailure(signInCall, err)
	}
	defer resp.Body.Close()

	reply, err := readReply(resp)
	if err != nil {
		return nil, transportFailure(signInCall, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, googleFailure(signInCall, resp, reply)
	}

	var result SignInResponse
	if err := json.Unmarshal(reply, &result); err != nil || result.IDToken == "" {
		// A 200 that is not a token is not a sign-in, whoever sent it. Accepting it hands an empty
		// bearer token to conductor, which answers 401, which reads as a refusal one layer up.
		return nil, interceptedFailure(signInCall, resp, reply)
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
		return nil, transportFailure(refreshCall, err)
	}
	defer resp.Body.Close()

	reply, err := readReply(resp)
	if err != nil {
		return nil, transportFailure(refreshCall, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, googleFailure(refreshCall, resp, reply)
	}

	var result RefreshResponse
	if err := json.Unmarshal(reply, &result); err != nil || result.IDToken == "" {
		return nil, interceptedFailure(refreshCall, resp, reply)
	}

	return &result, nil
}

// GetIDToken is a convenience function that refreshes and returns a Firebase ID token.
// Returns an error if the refresh token or API key is empty, or if the refresh fails.
func GetIDToken(refreshToken, apiKey string) (string, error) {
	if refreshToken == "" || apiKey == "" {
		return "", ErrNotAuthenticated
	}

	resp, err := RefreshIDToken(refreshToken, apiKey)
	if err != nil {
		return "", err
	}

	return resp.IDToken, nil
}

// ExtractFirebaseUID decodes the Firebase ID token's JWT payload and
// returns the `user_id` claim (Firebase's stable per-account uid). No
// signature verification — the token was minted by Google's token
// endpoint via our own refresh flow, so trust is established at the
// transport layer; reading our own uid out of it doesn't need crypto.
// Used to default the positional argument on commands keyed on the
// caller's own uid (e.g. `user permissions`) so an LLM/MCP user doesn't
// have to remember and paste their own uid. Returns "" when the token
// is malformed or doesn't carry a `user_id` claim (falls back to the
// caller's existing missing-arg path). Regression target: I12-D.
func ExtractFirebaseUID(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		UserID string `json:"user_id"`
		Sub    string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if claims.UserID != "" {
		return claims.UserID
	}
	return claims.Sub
}
