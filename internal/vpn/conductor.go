package vpn

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/runos-official/cli/internal/api"
)

// conductorClient is the daemon's narrow view of Conductor: it holds a device SESSION token, not
// a Firebase credential, and speaks exactly the three device routes a running tunnel needs. The
// CLI does enrolment and the session mint (they need the person's sign-in); the daemon only
// polls state, sets the connected set, and ends the session.

type conductorClient struct {
	api   *api.Client
	token string
	aid   string
	id    string
}

func newConductorClient(baseURL, aid, deviceID, sessionToken string) *conductorClient {
	return &conductorClient{
		api:   api.NewClient(baseURL),
		token: sessionToken,
		aid:   aid,
		id:    deviceID,
	}
}

func (c *conductorClient) statePath() string {
	return fmt.Sprintf("/%s/vpn/devices/%s/state", url.PathEscape(c.aid), url.PathEscape(c.id))
}

func (c *conductorClient) clustersPath() string {
	return fmt.Sprintf("/%s/vpn/devices/%s/clusters", url.PathEscape(c.aid), url.PathEscape(c.id))
}

func (c *conductorClient) sessionPath() string {
	return fmt.Sprintf("/%s/vpn/devices/%s/session", url.PathEscape(c.aid), url.PathEscape(c.id))
}

// stateResult is what a poll returns: the document when it changed, notModified when the ETag
// matched, or an error. A 401 carries loginRequired so the daemon tears down rather than looping.
type stateResult struct {
	doc           *Document
	notModified   bool
	loginRequired bool
	statusCode    int
}

// pollState fetches the desired-state document, sending If-None-Match so an unchanged document is
// a cheap 304. It uses api.Client.Do, which does not carry request headers, so the conditional
// request is issued directly here through the same client's transport.
func (c *conductorClient) pollState(etag string) (stateResult, error) {
	req, err := http.NewRequest(http.MethodGet, c.api.BaseURL()+c.statePath(), nil)
	if err != nil {
		return stateResult{}, fmt.Errorf("build state request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.api.HTTP().Do(req)
	if err != nil {
		return stateResult{}, fmt.Errorf("state request: %w", err)
	}
	defer resp.Body.Close()
	res := stateResult{statusCode: resp.StatusCode}
	switch resp.StatusCode {
	case http.StatusNotModified:
		res.notModified = true
		return res, nil
	case http.StatusUnauthorized:
		// The session lapsed or was revoked: tell the daemon to tear down and stop polling.
		res.loginRequired = true
		return res, nil
	case http.StatusOK:
		var doc Document
		if err := decodeBody(resp.Body, &doc); err != nil {
			return res, fmt.Errorf("parse state: %w", err)
		}
		res.doc = &doc
		return res, nil
	default:
		return res, fmt.Errorf("state request returned HTTP %d", resp.StatusCode)
	}
}

// setClusters PUTs the full connected set and returns the fresh document the response implies via
// a follow-up poll (the PUT response carries the device, not the whole document).
func (c *conductorClient) setClusters(cids []string) error {
	result, err := c.api.Do(http.MethodPut, c.clustersPath(), c.token, map[string]any{"cids": cids})
	if err != nil {
		return err
	}
	if !result.OK() {
		if msg := result.ErrorMessage(); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("set clusters returned HTTP %d", result.StatusCode)
	}
	return nil
}

// endSession ends the session server-side so the leases are removed. A lapsed token is accepted
// by Conductor for this, which is what lets a daemon tearing down after expiry end cleanly.
func (c *conductorClient) endSession() error {
	result, err := c.api.Do(http.MethodDelete, c.sessionPath(), c.token, nil)
	if err != nil {
		return err
	}
	if !result.OK() && result.StatusCode != http.StatusUnauthorized {
		if msg := result.ErrorMessage(); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("end session returned HTTP %d", result.StatusCode)
	}
	return nil
}
