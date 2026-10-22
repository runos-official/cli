package roidauth

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/api"
)

// DiscoveryPath is where an issuer publishes its OpenID discovery document.
const DiscoveryPath = "/.well-known/openid-configuration"

/*
CheckIssuer asks the issuer for its discovery document and returns nil only when the answer is one
this CLI can sign in against.

It exists because a CDN config can name an issuer before that issuer is live. Choosing roid for
such an environment sends the person to a service that does not answer. The check is made once,
before a new sign-in starts, and never for a sign-in that already exists (see cmd/login.go).

The document must name the same issuer that the config names, so a host that answers for another
issuer is refused (OIDC Discovery 1, section 4.3), and it must advertise the device endpoint this
CLI calls. The answer is not followed across a redirect: the issuer's own host serves the document.
The check sends no credential and no proof.
*/
func CheckIssuer(issuer string, timeout time.Duration) error {
	iss := strings.TrimRight(issuer, "/")
	c := api.NewClientWithTimeout(iss, timeout)
	c.HTTP().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Send(api.Request{Method: http.MethodGet, Path: DiscoveryPath})
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", iss, err)
	}
	if !res.OK() {
		return fmt.Errorf("%s answered HTTP %d for its discovery document", iss, res.StatusCode)
	}
	var doc struct {
		Issuer         string `json:"issuer"`
		DeviceEndpoint string `json:"device_authorization_endpoint"`
	}
	if err := res.Decode(&doc); err != nil {
		return fmt.Errorf("%s did not answer a discovery document: %w", iss, err)
	}
	if strings.TrimRight(doc.Issuer, "/") != iss {
		return fmt.Errorf("the discovery document at %s names issuer %q, not %q", iss, doc.Issuer, iss)
	}
	if doc.DeviceEndpoint != iss+"/device/authorize" {
		return fmt.Errorf("the discovery document at %s does not offer the device sign-in endpoint (got %q)", iss, doc.DeviceEndpoint)
	}
	return nil
}
