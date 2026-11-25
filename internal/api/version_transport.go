package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"github.com/runos-official/cli/version"
)

/*
UserAgent is how the CLI announces its version to RunOS: runos-cli/<version> (<os>/<arch>).

It is the User-Agent and not a custom header because the API gateway drops every X-RunOS-* header
except the ones it owns. Conductor reads the text after the prefix "runos-cli/" up to the first
space as the version, so the version must stay first and the platform must follow a space. A dev
build sends its own label ("dev" or a dev-<time> stamp), as it reports in version-check.
*/
func UserAgent() string {
	return "runos-cli/" + version.Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// WithoutVersion returns a copy of c that does not announce the CLI. A call that leaves RunOS (a
// release download, a server-supplied upload URL on another host) uses it, so the version goes to
// RunOS only. The copy shares c's timeout and redirect rules.
func WithoutVersion(c *http.Client) *http.Client {
	plain := *c
	if vt, ok := c.Transport.(versionTransport); ok {
		plain.Transport = vt.inner // the client's own transport, or nil for the default
	}
	return &plain
}

// maxGateBody bounds how much of a 426 body is read to look for the gate's code.
const maxGateBody = 64 * 1024

/*
NewTransport is the one place the CLI User-Agent and the update-required answer are handled.

Every client that talks to conductor sets it as its Transport, so the header cannot be missed by one
call path and the gate's answer cannot be shown as a raw HTTP body by one of them.
TestEveryHTTPClientIsStampedOrMarkedThirdParty keeps a new client from skipping it.

It wraps http.DefaultTransport as it is when the request is made, not when the client is built,
because the roid proof transport replaces http.DefaultTransport after clients exist.

A 426 whose JSON body has code cli.update_required comes back as an *UpdateRequiredError from the
client's Do, wrapped by net/http in a *url.Error, so callers reach it with errors.As. Any other 426
is an ordinary response with its body intact.
*/
func NewTransport() http.RoundTripper { return versionTransport{} }

// versionTransport stamps the User-Agent and reads the gate's answer, then hands the request to
// inner, or to http.DefaultTransport as it is at call time when inner is nil.
type versionTransport struct{ inner http.RoundTripper }

func (t versionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", UserAgent())
	next := t.inner
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUpgradeRequired {
		return resp, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGateBody))
	resp.Body.Close()
	if readErr == nil {
		if gate := parseUpdateRequired(body); gate != nil {
			notifyUpdateRequired()
			return nil, gate
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func parseUpdateRequired(body []byte) *UpdateRequiredError {
	var fields map[string]any
	if json.Unmarshal(body, &fields) != nil || fields["code"] != UpdateRequiredCode {
		return nil
	}
	return &UpdateRequiredError{fields: fields}
}

// ForTarget returns c for a request to RunOS's own API (target on the same host as base) and
// WithoutVersion(c) for any other host. A server-supplied URL, such as a build upload, may point
// elsewhere, and the version must not go with it.
func ForTarget(c *http.Client, base, target string) *http.Client {
	b, errBase := url.Parse(base)
	t, errTarget := url.Parse(target)
	if errBase == nil && errTarget == nil && b.Host != "" && strings.EqualFold(b.Host, t.Host) {
		return c
	}
	return WithoutVersion(c)
}
