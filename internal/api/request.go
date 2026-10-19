package api

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

// maxResponseBytes bounds a decoded response body. The Procedure
// approval render is the largest thing this client reads and it is a
// few kilobytes; a cap keeps a misrouted request to some other host from
// filling memory before it fails to parse.
const maxResponseBytes = 4 * 1024 * 1024

// Result is one completed HTTP exchange: the status line's code and the
// raw body, undecoded.
//
// The body is returned RAW rather than decoded into a success type
// because callers on the Procedure surface must read a non-2xx body as
// carefully as a 2xx one. A blocked plan is a 409 carrying every failing
// reason, the classification and the plan hash; a client that discarded
// the body on non-2xx would turn the only explanation a user gets into
// "request failed".
type Result struct {
	StatusCode int
	Body       []byte
	// Response headers, kept because some facts ride one rather than the body. The session expiry
	// is stamped on EVERY authenticated response so a client learns it without a second round trip
	// (conductor's X-RunOS-Session-Expires-At); discarding headers here would mean a new endpoint
	// and one more thing to keep in step.
	Header http.Header
}

// SessionExpiredCode is conductor's machine-readable reason for a sign-in that has aged out. A
// caller branches on this rather than on the sentence, which is written for a person and is
// expected to be reworded.
const SessionExpiredCode = "auth.session_expired"

// SessionExpiresHeader carries when the current sign-in stops being accepted, RFC3339. Absent means
// no known expiry (a personal access token, or the rule switched off), never "expires now".
const SessionExpiresHeader = "X-RunOS-Session-Expires-At"

// SessionExpired reports whether conductor refused this request because the sign-in has aged out,
// which is a different thing from a bad or missing credential and has a different fix.
func (r *Result) SessionExpired() bool {
	if r.StatusCode != http.StatusUnauthorized {
		return false
	}
	var envelope struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(r.Body, &envelope) != nil {
		return false
	}
	return envelope.Code == SessionExpiredCode
}

// SessionExpiresAt returns when the sign-in stops being accepted, or the zero time when the
// response did not say.
func (r *Result) SessionExpiresAt() time.Time {
	if r.Header == nil {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, r.Header.Get(SessionExpiresHeader))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// OK reports whether the status is 2xx.
func (r *Result) OK() bool {
	return r.StatusCode >= 200 && r.StatusCode < 300
}

// Decode unmarshals the body into out. Returns an error naming the
// status code when the body is not the JSON the caller expected, so a
// proxy's HTML error page does not surface as a bare syntax error.
func (r *Result) Decode(out any) error {
	if len(r.Body) == 0 {
		return fmt.Errorf("empty response body (HTTP %d)", r.StatusCode)
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return fmt.Errorf("could not parse the response (HTTP %d): %w", r.StatusCode, err)
	}
	return nil
}

// ErrorMessage extracts the conductor error envelope's human-readable
// text, preferring `error` then `reason` then `message`. Returns "" when
// the body carries none of them, so a caller can fall back to its own
// wording rather than printing an empty line.
func (r *Result) ErrorMessage() string {
	var envelope struct {
		Error   string `json:"error"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if json.Unmarshal(r.Body, &envelope) != nil {
		return ""
	}
	switch {
	case envelope.Error != "" && envelope.Reason != "":
		return envelope.Error + ": " + envelope.Reason
	case envelope.Error != "":
		return envelope.Error
	case envelope.Reason != "":
		return envelope.Reason
	default:
		return envelope.Message
	}
}

// Do performs an authenticated JSON request against the Conductor API
// and returns the completed exchange. path is joined to the client's
// base URL and must already be escaped by the caller (use url.PathEscape
// on every id segment). body is marshalled as JSON when non-nil.
//
// A non-2xx is NOT an error here: it is a Result the caller reads. Only
// a transport, marshalling or read failure returns err.
func (c *Client) Do(method, path, token string, body any) (*Result, error) {
	return c.Send(Request{Method: method, Path: path, Token: token, JSON: body})
}

// Request is one call for Send. JSON and Form are the two body shapes; a
// request carries at most one. Header adds headers after the defaults.
type Request struct {
	Method string
	Path   string
	// Token is sent as "Authorization: Bearer <token>". Empty sends no
	// Authorization header, for a public endpoint.
	Token  string
	JSON   any
	Form   url.Values
	Header http.Header
}

// Send performs one request and returns the completed exchange, with the
// same contract as Do: a non-2xx is a Result, not an error. It exists for
// the calls Do cannot express, such as an OAuth endpoint that takes a form
// body and a proof header.
func (c *Client) Send(r Request) (*Result, error) {
	if r.JSON != nil && r.Form != nil {
		return nil, fmt.Errorf("a request carries one body: JSON or a form, not both")
	}
	var reader io.Reader
	contentType := ""
	switch {
	case r.JSON != nil:
		encoded, err := json.Marshal(r.JSON)
		if err != nil {
			return nil, fmt.Errorf("could not encode the request body: %w", err)
		}
		reader, contentType = bytes.NewReader(encoded), "application/json"
	case r.Form != nil:
		reader, contentType = strings.NewReader(r.Form.Encode()), "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequest(r.Method, strings.TrimRight(c.baseURL, "/")+r.Path, reader)
	if err != nil {
		return nil, fmt.Errorf("could not build the request: %w", err)
	}
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("could not read the response: %w", err)
	}
	return &Result{StatusCode: resp.StatusCode, Body: raw, Header: resp.Header}, nil
}
