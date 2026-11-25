// Package apitest lets a test see which requests carry the CLI's User-Agent, with no network.
package apitest

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Sent is one request that reached the network layer.
type Sent struct {
	Host      string
	UserAgent string
}

// Recorder remembers what reached http.DefaultTransport.
type Recorder struct {
	mu   sync.Mutex
	sent []Sent
}

// Sent returns the requests seen so far, in order.
func (r *Recorder) Sent() []Sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Sent(nil), r.sent...)
}

func (r *Recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.sent = append(r.sent, Sent{Host: req.URL.Host, UserAgent: req.UserAgent()})
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
}

// Record replaces http.DefaultTransport for the test with a recorder that answers 404 to every
// request, and puts the old one back afterwards. Do not run such a test in parallel.
func Record(t *testing.T) *Recorder {
	t.Helper()
	r := &Recorder{}
	previous := http.DefaultTransport
	http.DefaultTransport = r
	t.Cleanup(func() { http.DefaultTransport = previous })
	return r
}

// CarriesCLIVersion reports whether the request announced itself as the RunOS CLI.
func (s Sent) CarriesCLIVersion() bool { return strings.HasPrefix(s.UserAgent, "runos-cli/") }
