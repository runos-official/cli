package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
)

// The CLI half of the RunOS VPN's conductor calls: enrolment and the session mint. Both need the
// PERSON's interactive Firebase token (the daemon holds only a device session token), so they
// live here in the CLI process, not in the root daemon. The daemon does state polling and PUT
// clusters with its session token; see internal/vpn/conductor.go.

// vpnDeviceView is the device shape the enrol endpoint returns (a subset; the CLI needs the id).
type vpnDeviceView struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	PublicKey string `json:"publicKey"`
}

// errKeyRevoked is enrolDevice's answer when conductor refuses the key because it was revoked
// (409 `vpn.key_revoked`): the machine needs a new keypair, which `up` asks the daemon for.
var errKeyRevoked = errors.New("this machine's VPN key was revoked")

/*
enrolDevice enrols this machine's public key, idempotent on the key.

The middle return is "sign in first, then try again", the same signal mintSession gives. It exists
because conductor now expires a Firebase sign-in, and this is the FIRST call `vpn up` makes: a
refusal here used to end the command, which meant the session expiring broke the very command that
fixes it. The remedy for an aged-out session is a sign-in, and `up` already knows how to do one.
*/
func enrolDevice(cfg *config.Config, token, publicKey, name, osName string) (*vpnDeviceView, bool, error) {
	client := api.NewClient(cfg.GetAPIURL())
	path := "/" + url.PathEscape(cfg.GetAccountID()) + "/vpn/devices"
	result, err := client.Do(http.MethodPost, path, token, map[string]any{
		"publicKey": publicKey, "name": name, "os": osName,
	})
	if err != nil {
		return nil, false, err
	}
	if result.SessionExpired() {
		return nil, true, nil
	}
	if result.StatusCode == http.StatusConflict && vpnErrorCode(result) == "vpn.key_revoked" {
		return nil, false, errKeyRevoked
	}
	if !result.OK() {
		return nil, false, fmt.Errorf("%s", vpnErrorMessage(result, "enrol this device"))
	}
	var body struct {
		Device vpnDeviceView `json:"device"`
	}
	if err := result.Decode(&body); err != nil {
		return nil, false, err
	}
	return &body.Device, false, nil
}

// mintedSession is what the session endpoint returns.
type mintedSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// mintSession mints a 24-hour session for a device. Conductor requires the caller's Firebase
// auth_time to be within 5 minutes; signInRequired reports the refusal so `up` can re-sign-in
// and retry, rather than the caller decoding auth_time itself.
func mintSession(cfg *config.Config, token, deviceID string) (*mintedSession, bool, error) {
	client := api.NewClient(cfg.GetAPIURL())
	path := "/" + url.PathEscape(cfg.GetAccountID()) + "/vpn/devices/" + url.PathEscape(deviceID) + "/session"
	result, err := client.Do(http.MethodPost, path, token, nil)
	if err != nil {
		return nil, false, err
	}
	// Two refusals, one remedy. `vpn.sign_in_required` is conductor asking for a sign-in from the
	// last few minutes; `auth.session_expired` is the whole session having aged out. Both are fixed
	// by signing in, and telling them apart here would only give `up` two branches that do the same
	// thing.
	if result.SessionExpired() ||
		(result.StatusCode == http.StatusForbidden && vpnErrorCode(result) == "vpn.sign_in_required") {
		return nil, true, nil
	}
	if !result.OK() {
		return nil, false, fmt.Errorf("%s", vpnErrorMessage(result, "start a VPN session"))
	}
	var session mintedSession
	if err := result.Decode(&session); err != nil {
		return nil, false, err
	}
	return &session, false, nil
}

// vpnErrorMessage renders a conductor error envelope, falling back to the status code.
func vpnErrorMessage(result *api.Result, action string) string {
	if msg := result.ErrorMessage(); msg != "" {
		return msg
	}
	return fmt.Sprintf("could not %s (HTTP %d)", action, result.StatusCode)
}

// vpnErrorCode extracts the stable machine `code` from a conductor error envelope.
func vpnErrorCode(result *api.Result) string {
	var envelope struct {
		Code string `json:"code"`
	}
	_ = result.Decode(&envelope)
	return envelope.Code
}
