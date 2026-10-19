package roidauth

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// registry maps the access tokens this process minted to the key that
// proves them and the hosts allowed to see the proofs.
type registry struct {
	mu      sync.Mutex
	entries map[string]regEntry
}

type regEntry struct {
	key   *Key
	exp   time.Time
	hosts map[string]bool
}

var defaultRegistry = &registry{}

func (r *registry) add(token string, key *Key, exp time.Time, hosts ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]regEntry{}
	}
	now := time.Now()
	for t, e := range r.entries {
		if now.After(e.exp) {
			delete(r.entries, t)
		}
	}
	hs := map[string]bool{}
	for _, h := range hosts {
		if h != "" {
			hs[h] = true
		}
	}
	r.entries[token] = regEntry{key: key, exp: exp, hosts: hs}
}

func (r *registry) remove(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, token)
}

func (r *registry) lookup(token, host string) (*Key, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[token]
	if !ok || !e.hosts[host] {
		return nil, false
	}
	return e.key, true
}

// proofTransport adds a fresh access proof to each request that carries
// one of this process's roid access tokens to an allowed host. Every
// command's HTTP calls pass through it, so no call site has to know that
// roid tokens need a proof. A request that already carries a proof keeps
// it.
type proofTransport struct {
	base http.RoundTripper
	reg  *registry
}

func (t *proofTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	auth := req.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(auth, "Bearer "); ok && strings.HasPrefix(tok, "at_") && req.Header.Get(ProofHeader) == "" {
		if key, ok := t.reg.lookup(tok, req.URL.Host); ok {
			// The proof names this request: its method and URL (FPL321 c5).
			h, err := key.header(purposeAccess, subjectHash(tok), req.Method, req.URL.String(), time.Now())
			if err != nil {
				return nil, err
			}
			req = req.Clone(req.Context())
			req.Header.Set(ProofHeader, h)
		}
	}
	return t.base.RoundTrip(req)
}

var installOnce sync.Once

// InstallProofTransport wraps http.DefaultTransport with the proof
// transport, once per process. Clients built with no Transport of their
// own, which is every client in this CLI, then prove each roid token use.
func InstallProofTransport() {
	installOnce.Do(func() {
		http.DefaultTransport = &proofTransport{base: http.DefaultTransport, reg: defaultRegistry}
	})
}
