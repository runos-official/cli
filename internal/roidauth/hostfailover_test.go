package roidauth

import (
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/roidapi"
)

const k8sTokenPath = "/v1/accounts/acct1/clusters/cl1/k8s/token"

// signedIn signs the fake's person in with a session that may prove to both hosts, as auth.RoidSession
// builds it, and returns the access token. identityHost is where the session reads who it is: the
// API host on the prod shape, the issuer on the dev shape (the host that serves /v1/me there).
func signedIn(t *testing.T, f *fakeRoid, identityHost string) string {
	t.Helper()
	c := NewClientWithAPI(f.srv.URL, identityHost)
	c.Sleep = noSleep
	iss, _ := url.Parse(f.srv.URL)
	apiu, _ := url.Parse(f.api.URL)
	s := &Session{Issuer: f.srv.URL, Store: newMemStore(), LockPath: filepath.Join(t.TempDir(), "roid.lock"), Client: c,
		ProofHosts: []string{apiu.Host, iss.Host}}
	login(t, f, s)
	InstallProofTransport() // what auth.RoidSession does: every client in the CLI proves roid token use
	at, err := s.Bearer()
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func postToken(t *testing.T, r *roidapi.Router, at string) *api.Result {
	t.Helper()
	res, err := r.Send(api.Request{Method: http.MethodPost, Path: k8sTokenPath, Token: at})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// PROD shape: the gateway serves the token route, the issuer answers 404 not_on_this_host. One call, to
// the gateway, with a proof that names the gateway.
func TestProdShapeTokenCallIsProvedForTheGatewayAndNotRetried(t *testing.T) {
	f := newFakeRoid(t)
	f.splitAPIHost()
	at := signedIn(t, f, f.api.URL)
	res := postToken(t, roidapi.NewRouter(f.api.URL, f.srv.URL), at)
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, res.Body)
	}
	if hits := f.hitsAt(f.api.URL, k8sTokenPath); len(hits) != 1 || hits[0].ProofTarget != f.api.URL+k8sTokenPath {
		t.Errorf("gateway hits = %+v, want one with a proof for the gateway origin", hits)
	}
	if hits := f.hitsAt(f.srv.URL, k8sTokenPath); len(hits) != 0 {
		t.Errorf("the issuer got %+v; nothing may go there when the gateway serves the route", hits)
	}
}

// DEV shape: the gateway (conductor) answers 404, the issuer serves the route. The retry is a new
// request with its own proof for the issuer origin: the first proof names the gateway and is useless at
// the issuer, and the fake verifies the proof it receives before it answers 200.
func TestDevShapeTokenCallRetriesOnTheIssuerWithAProofForTheIssuer(t *testing.T) {
	f := newFakeRoid(t)
	f.splitDevHosts()
	at := signedIn(t, f, f.srv.URL)
	r := roidapi.NewRouter(f.api.URL, f.srv.URL)
	res := postToken(t, r, at)
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, res.Body)
	}
	g, i := f.hitsAt(f.api.URL, k8sTokenPath), f.hitsAt(f.srv.URL, k8sTokenPath)
	if len(g) != 1 || g[0].Status != 404 || g[0].ProofTarget != f.api.URL+k8sTokenPath {
		t.Errorf("gateway hits = %+v, want one 404 carrying a proof for the gateway origin", g)
	}
	if len(i) != 1 || i[0].Status != 200 || i[0].ProofTarget != f.srv.URL+k8sTokenPath {
		t.Errorf("issuer hits = %+v, want one 200 carrying a proof for the issuer origin", i)
	}
	// Later calls of the run go straight to the host that worked, each with a fresh proof.
	for n := 0; n < 2; n++ {
		if res := postToken(t, r, at); res.StatusCode != 200 {
			t.Fatalf("later call %d: status %d", n, res.StatusCode)
		}
	}
	if g := f.hitsAt(f.api.URL, k8sTokenPath); len(g) != 1 {
		t.Errorf("the gateway was tried %d times in the run, want 1", len(g))
	}
	if i := f.hitsAt(f.srv.URL, k8sTokenPath); len(i) != 3 {
		t.Errorf("the issuer got %d calls, want 3", len(i))
	}
}

// A 401 from the first host (a bad token) is the answer. It is not a reason to try the other host.
func TestAnUnauthorizedTokenCallIsNotRetried(t *testing.T) {
	f := newFakeRoid(t)
	f.splitAPIHost()
	signedIn(t, f, f.api.URL)
	res := postToken(t, roidapi.NewRouter(f.api.URL, f.srv.URL), "at_not_a_token_of_this_fake")
	if res.StatusCode != 401 {
		t.Fatalf("status %d, want 401", res.StatusCode)
	}
	if hits := f.hitsAt(f.srv.URL, k8sTokenPath); len(hits) != 0 {
		t.Errorf("a 401 on the gateway sent the bearer to the issuer: %+v", hits)
	}
}
