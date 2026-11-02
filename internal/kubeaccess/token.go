// Package kubeaccess lets a signed-in person use kubectl with their own RunOS
// identity. It asks the identity service for a short Kubernetes token
// (POST /v1/accounts/{acct}/clusters/{cluster}/k8s/token), prints it as a
// client.authentication.k8s.io/v1 ExecCredential, turns a refusal into one
// plain line, and writes the kubeconfig entry that makes kubectl call the CLI.
// It never writes a token to disk: a token lives in memory for the process.
package kubeaccess

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/runos-official/cli/internal/api"
)

// Skew is how long before expiry a cached token is replaced.
const Skew = 30 * time.Second

// Issued is one Kubernetes token and the time it stops working.
type Issued struct {
	Token     string
	ExpiresAt time.Time
}

// Fetcher asks the identity service for a token for one person on one
// cluster. Bearer returns the person's CLI access token for each call; the
// proof transport the sign-in installs adds the per-request proof.
type Fetcher struct {
	BaseURL string
	// Send, when set, carries the request instead of a plain call to BaseURL. The CLI sets it to the
	// host router (roidapi.Router.Send), which tries the API host and, after a 404, the sign-in host.
	Send    func(api.Request) (*api.Result, error)
	Account string
	Cluster string
	Bearer  func() (string, error)
}

// Fetch makes one token call. A refusal is a *Refusal. A session problem is
// the error Bearer returned, unchanged.
func (f *Fetcher) Fetch() (Issued, error) {
	tok, err := f.Bearer()
	if err != nil {
		return Issued{}, err
	}
	path := "/v1/accounts/" + url.PathEscape(f.Account) + "/clusters/" + url.PathEscape(f.Cluster) + "/k8s/token"
	send := f.Send
	if send == nil {
		send = api.NewClient(f.BaseURL).Send
	}
	res, err := send(api.Request{Method: http.MethodPost, Path: path, Token: tok})
	if err != nil {
		return Issued{}, &TransportError{Err: err}
	}
	if !res.OK() {
		return Issued{}, newRefusal(res)
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := res.Decode(&body); err != nil {
		return Issued{}, err
	}
	exp, perr := time.Parse(time.RFC3339, body.ExpiresAt)
	if body.Token == "" || perr != nil {
		return Issued{}, fmt.Errorf("the identity service answered without a usable token (HTTP %d)", res.StatusCode)
	}
	return Issued{Token: body.Token, ExpiresAt: exp.UTC()}, nil
}

// TransportError means the request did not complete: no answer, not a refusal.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// Source hands out a token and keeps it in memory until Skew before it
// expires. The cache lives as long as the Source and is never written out.
type Source struct {
	Fetcher *Fetcher
	// Now is time.Now outside tests.
	Now func() time.Time

	mu     sync.Mutex
	cached *Issued
}

// Token returns the cached token or fetches a new one. A refusal is not
// cached.
func (s *Source) Token() (Issued, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.Now().Before(s.cached.ExpiresAt.Add(-Skew)) {
		return *s.cached, nil
	}
	s.cached = nil
	iss, err := s.Fetcher.Fetch()
	if err != nil {
		return Issued{}, err
	}
	s.cached = &iss
	return iss, nil
}

// decodeError reads roid's refusal body. Its fields are all optional.
type decodeError struct {
	Error struct {
		Code        string `json:"code"`
		Reason      string `json:"reason"`
		Message     string `json:"message"`
		Detail      string `json:"detail"`
		Action      string `json:"action"`
		Requirement string `json:"requirement"`
	} `json:"error"`
}

func newRefusal(res *api.Result) *Refusal {
	var b decodeError
	_ = json.Unmarshal(res.Body, &b)
	e := b.Error
	msg := e.Message
	if msg == "" {
		msg = e.Detail
	}
	return &Refusal{
		Status: res.StatusCode, Code: e.Code, Reason: e.Reason, Message: msg,
		Requirement: e.Requirement, RetryAfter: res.Header.Get("Retry-After"),
	}
}
