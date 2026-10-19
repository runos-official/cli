// Package roidauth is the CLI side of sign-in through roid, the RunOS
// identity service: the device flow, the proof key that binds every token
// to this install, refresh rotation, logout, and the transport that proves
// possession of the key on each API call.
//
// It is used for an environment whose config names an auth issuer. Other
// environments keep the Firebase sign-in in internal/auth.
package roidauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/api"
)

// ClientID is the OAuth client id roid registers for the CLI.
const ClientID = "runos-cli"

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// minPollInterval is the RFC 8628 floor between polls.
const minPollInterval = 5 * time.Second

// Error is an OAuth error answer from roid.
type Error struct {
	Status      int
	Code        string
	Description string
}

func (e *Error) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// DeviceStart is roid's answer to a device authorization request.
type DeviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Tokens is a token answer: a short-lived access token and the refresh
// token that replaces itself on every use.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	CredentialID string `json:"credential_id"`
}

// Identity is who the session signs in as.
type Identity struct {
	PersonID  string   `json:"person_id"`
	Method    string   `json:"method"`
	Assurance int      `json:"assurance"`
	Accounts  []string `json:"accounts"`
}

// Client calls roid's CLI endpoints through internal/api.
type Client struct {
	api    *api.Client
	issuer string // the issuer URL; proofs name it with each path
	Now    func() time.Time
	Sleep  func(time.Duration)
}

// NewClient returns a client for the roid issuer at issuerURL.
func NewClient(issuerURL string) *Client {
	iss := strings.TrimRight(issuerURL, "/")
	return &Client{api: api.NewClient(iss), issuer: iss, Now: time.Now, Sleep: time.Sleep}
}

// proof is the ProofHeader for one request to roid: method and path on
// the issuer (FPL321 c5).
func (c *Client) proof(key *Key, purpose, subject, method, path string) (http.Header, error) {
	h, err := key.header(purpose, subject, method, c.issuer+path, c.Now())
	if err != nil {
		return nil, err
	}
	return proofOnly(h), nil
}

// call sends one request and turns a non-2xx answer into an *Error.
func (c *Client) call(r api.Request, out any) error {
	res, err := c.api.Send(r)
	if err != nil {
		return fmt.Errorf("could not reach the RunOS identity service: %w", err)
	}
	if !res.OK() {
		e := &Error{Status: res.StatusCode}
		var envelope struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
			Code        string `json:"code"`
		}
		if res.Decode(&envelope) == nil {
			e.Code, e.Description = envelope.Error, envelope.Description
			if e.Code == "" {
				e.Code = envelope.Code
			}
		}
		if e.Code == "" {
			e.Code = fmt.Sprintf("http_%d", res.StatusCode)
		}
		return e
	}
	if out == nil {
		return nil
	}
	return res.Decode(out)
}

func proofOnly(v string) http.Header { return http.Header{ProofHeader: {v}} }

// StartDevice opens a device authorization bound to key.
func (c *Client) StartDevice(key *Key) (DeviceStart, error) {
	var d DeviceStart
	err := c.call(api.Request{Method: http.MethodPost, Path: "/device/authorize",
		Form: url.Values{"client_id": {ClientID}, "dpop_jkt": {key.Thumbprint()}}}, &d)
	return d, err
}

// PollDevice polls until the person approves, the code expires, or roid
// refuses. onPending runs after each authorization_pending answer.
func (c *Client) PollDevice(ctx context.Context, d DeviceStart, key *Key, onPending func()) (Tokens, error) {
	interval := time.Duration(d.Interval) * time.Second
	if interval < minPollInterval {
		interval = minPollInterval
	}
	deadline := c.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for c.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return Tokens{}, err
		}
		c.Sleep(interval)
		var t Tokens
		hdr, err := c.proof(key, purposeDevicePoll, subjectHash(d.DeviceCode), http.MethodPost, "/device/token")
		if err != nil {
			return Tokens{}, err
		}
		err = c.call(api.Request{Method: http.MethodPost, Path: "/device/token",
			Form:   url.Values{"grant_type": {deviceGrantType}, "device_code": {d.DeviceCode}, "client_id": {ClientID}},
			Header: hdr}, &t)
		if err == nil {
			return t, nil
		}
		var oe *Error
		if !errors.As(err, &oe) {
			return Tokens{}, err
		}
		switch oe.Code {
		case "authorization_pending":
			if onPending != nil {
				onPending()
			}
		case "slow_down":
			interval += minPollInterval
		default:
			return Tokens{}, err
		}
	}
	return Tokens{}, &Error{Code: "expired_token", Description: "the sign-in code expired before it was approved"}
}

// Refresh spends a refresh token and returns its replacement.
func (c *Client) Refresh(refresh string, key *Key) (Tokens, error) {
	var t Tokens
	hdr, err := c.proof(key, purposeRefresh, subjectHash(refresh), http.MethodPost, "/cli/token")
	if err != nil {
		return Tokens{}, err
	}
	err = c.call(api.Request{Method: http.MethodPost, Path: "/cli/token",
		JSON:   map[string]string{"grant_type": "refresh_token", "refresh_token": refresh},
		Header: hdr}, &t)
	return t, err
}

// Logout revokes the whole token family the refresh token belongs to.
func (c *Client) Logout(refresh string) error {
	return c.call(api.Request{Method: http.MethodPost, Path: "/cli/logout", JSON: map[string]string{"token": refresh}}, nil)
}

// identity reads who the access token signs in as, and their accounts.
func (c *Client) identity(access string, key *Key) (Identity, error) {
	var id Identity
	hdr, err := c.proof(key, purposeAccess, subjectHash(access), http.MethodGet, "/v1/me")
	if err != nil {
		return Identity{}, err
	}
	if err := c.call(api.Request{Method: http.MethodGet, Path: "/v1/me", Token: access, Header: hdr}, &id); err != nil {
		return Identity{}, err
	}
	var accts struct {
		Accounts []string `json:"accounts"`
	}
	hdr, err = c.proof(key, purposeAccess, subjectHash(access), http.MethodGet, "/user/accounts")
	if err != nil {
		return Identity{}, err
	}
	if err := c.call(api.Request{Method: http.MethodGet, Path: "/user/accounts", Token: access, Header: hdr}, &accts); err != nil {
		return Identity{}, err
	}
	id.Accounts = accts.Accounts
	return id, nil
}
