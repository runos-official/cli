package roidapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/runos-official/cli/internal/api"
)

// Client calls the identity service's access API. Every request carries
// the person's identity-service access token; the proof transport that
// sign-in installs adds the per-request proof for the issuer host.
type Client struct {
	http  *api.Client
	token func() (string, error)
}

// New returns a client for the identity service at baseURL (see BaseURL).
// token returns the access token for each call.
func New(baseURL string, token func() (string, error)) *Client {
	return &Client{http: api.NewClient(baseURL), token: token}
}

// Call is one access API request. Op is the OpenAPI operationId. A change
// to an existing role, group or grant sends IfMatch; when IfMatch is empty
// the client reads the resource first and uses that ETag. StepUp names a
// completed security-key step-up for an escalation-class change.
type Call struct {
	Op      string
	Params  map[string]string
	Query   url.Values
	Body    any
	IfMatch string
	StepUp  string
}

// Do sends one call and returns the result on success. A refusal is one
// of ErrStale, *StepUpRequiredError or *APIError.
func (c *Client) Do(call Call) (*api.Result, error) {
	op, ok := operations[call.Op]
	if !ok {
		return nil, fmt.Errorf("unknown access operation %q", call.Op)
	}
	path, err := expand(op.path, call.Params)
	if err != nil {
		return nil, err
	}
	if len(call.Query) > 0 {
		path += "?" + call.Query.Encode()
	}
	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if op.etagFrom != "" {
		etag := call.IfMatch
		if etag == "" {
			if etag, err = c.etag(op.etagFrom, call.Params, tok); err != nil {
				return nil, err
			}
		}
		header.Set("If-Match", etag)
	}
	if call.StepUp != "" {
		header.Set("X-RunOS-Step-Up", call.StepUp)
	}
	res, err := c.http.Send(api.Request{Method: op.method, Path: path, Token: tok, JSON: call.Body, Header: header})
	if err != nil {
		return nil, err
	}
	if err := refusal(res); err != nil {
		return nil, err
	}
	return res, nil
}

// etag reads one resource and returns its ETag header.
func (c *Client) etag(readOp string, params map[string]string, tok string) (string, error) {
	path, err := expand(operations[readOp].path, params)
	if err != nil {
		return "", err
	}
	res, err := c.http.Send(api.Request{Method: http.MethodGet, Path: path, Token: tok})
	if err != nil {
		return "", err
	}
	if err := refusal(res); err != nil {
		return "", err
	}
	etag := res.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("the identity service gave no ETag for %s, so the change cannot be made safely", strings.TrimPrefix(path, "/v1/accounts/"))
	}
	return etag, nil
}

// expand fills each {name} in a path template with its escaped value and
// refuses a missing one.
func expand(tmpl string, params map[string]string) (string, error) {
	var b strings.Builder
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			b.WriteString(tmpl)
			return b.String(), nil
		}
		end := strings.IndexByte(tmpl[open:], '}')
		name := tmpl[open+1 : open+end]
		v := params[name]
		if v == "" {
			return "", fmt.Errorf("missing %s", name)
		}
		b.WriteString(tmpl[:open])
		b.WriteString(url.PathEscape(v))
		tmpl = tmpl[open+end+1:]
	}
}
