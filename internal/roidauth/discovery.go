package roidauth

import (
	"fmt"
	"net/http"
	"time"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
)

// DiscoveryPath is where an issuer publishes its OpenID discovery document.
const DiscoveryPath = "/.well-known/openid-configuration"

// IssuerNotDeployedError is the one answer that says the identity service is not deployed at the
// issuer: its host answers HTTP 404 or 410 for the discovery path, over a connection that passed
// TLS verification. Every other failure (a timeout, a reset, a refused connection, a TLS error, a
// 3xx, 4xx or 5xx, a document that does not parse or does not match) is NOT this error. The caller
// falls back to another sign-in on this error alone.
type IssuerNotDeployedError struct {
	Issuer string
	Status int
}

func (e *IssuerNotDeployedError) Error() string {
	return fmt.Sprintf("%s answered HTTP %d for its discovery document: the identity service is not deployed there", e.Issuer, e.Status)
}

/*
CheckIssuer asks the issuer for its discovery document and returns nil only when the answer is one
this CLI can sign in against.

It exists because a CDN config can name an issuer before that issuer is live. Choosing roid for
such an environment sends the person to a service that does not answer. The check is made once,
before a new sign-in starts, and never for a sign-in that already exists (see cmd/login.go).

A definitive "not deployed" answer (HTTP 404 or 410 from the issuer's own host) is returned as an
*IssuerNotDeployedError. Any other failure is a plain error: an issuer that is down, filtered or
answering badly is not an issuer that does not exist, and the caller must not treat it as one.

The issuer must be one the sign-in itself would accept (config.ValidateIssuer): https, or http on a
loopback host, an origin with no path or credentials. The check refuses anything else before it
opens a connection.

The document must name the same issuer that the config names, so a host that answers for another
issuer is refused (OIDC Discovery 1, section 4.3), and it must advertise the device endpoint this
CLI calls. A trailing slash and the case of the host are the only differences tolerated: the
document is checked and never used, every endpoint is built from the configured issuer. The answer
is not followed across a redirect: the issuer's own host serves the document. The check sends no
credential and no proof.
*/
func CheckIssuer(issuer string, timeout time.Duration) error {
	if err := config.ValidateIssuer(issuer); err != nil {
		return fmt.Errorf("not checking the identity service: %w", err)
	}
	iss := config.CanonicalIssuer(issuer)
	c := api.NewClientWithTimeout(iss, timeout)
	c.HTTP().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Send(api.Request{Method: http.MethodGet, Path: DiscoveryPath})
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", iss, err)
	}
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		return &IssuerNotDeployedError{Issuer: iss, Status: res.StatusCode}
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
	if config.CanonicalIssuer(doc.Issuer) != iss {
		return fmt.Errorf("the discovery document at %s names issuer %q, not %q", iss, doc.Issuer, iss)
	}
	if doc.DeviceEndpoint != iss+"/device/authorize" {
		return fmt.Errorf("the discovery document at %s does not offer the device sign-in endpoint (got %q)", iss, doc.DeviceEndpoint)
	}
	return nil
}
