package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("could not encode the request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, strings.TrimRight(c.baseURL, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("could not build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	return &Result{StatusCode: resp.StatusCode, Body: raw}, nil
}
