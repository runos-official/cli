package roidauth

import (
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
)

// loginSession is a session built like auth.RoidSession builds it: the client knows the issuer and the
// API address, and the proof hosts hold both. It makes no override of any kind.
func loginSession(t *testing.T, f *fakeRoid) *Session {
	t.Helper()
	c := NewClientWithAPI(f.srv.URL, f.api.URL)
	c.Sleep = noSleep
	iss, _ := url.Parse(f.srv.URL)
	apiu, _ := url.Parse(f.api.URL)
	return &Session{Issuer: f.srv.URL, Store: newMemStore(), LockPath: filepath.Join(t.TempDir(), "roid.lock"), Client: c,
		ProofHosts: []string{apiu.Host, iss.Host}}
}

// PROD shape: the API host serves /v1/me and /user/accounts (objects) and the issuer answers 404 for them.
// Login reads who you are on the API host, with a proof for that origin, and never asks the issuer.
func TestLoginReadsWhoYouAreOnTheAPIHostOnProd(t *testing.T) {
	f := newFakeRoid(t)
	f.splitAPIHost()
	id := login(t, f, loginSession(t, f))
	if id.PersonID != "p_alice" || !reflect.DeepEqual(id.Accounts, []string{"acct1"}) {
		t.Fatalf("identity = %+v", id)
	}
	for _, route := range []string{"/v1/me", "/user/accounts"} {
		hits := f.hitsAt(f.api.URL, route)
		if len(hits) != 1 || hits[0].Status != 200 || hits[0].ProofTarget != f.api.URL+route {
			t.Errorf("API host %s hits = %+v, want one 200 with a proof for the API origin", route, hits)
		}
		if hits := f.hitsAt(f.srv.URL, route); len(hits) != 0 {
			t.Errorf("issuer %s hits = %+v, want none", route, hits)
		}
	}
}

// DEV shape: the API host is conductor and answers 404 for /v1/me; the issuer serves /v1/me and its own copy
// of /user/accounts (bare ids). `runos login` works with no RUNOS_API_URL: one 404 on the API host, then the
// issuer, each request with a proof built for the origin it goes to. The 404 is learned once: /user/accounts
// goes straight to the issuer.
func TestLoginReadsWhoYouAreOnTheIssuerOnDevWithNoOverride(t *testing.T) {
	f := newFakeRoid(t)
	f.splitDevHosts()
	s := loginSession(t, f)
	id := login(t, f, s)
	if id.PersonID != "p_alice" || !reflect.DeepEqual(id.Accounts, []string{"acct1"}) {
		t.Fatalf("identity = %+v", id)
	}
	me := f.hitsAt(f.api.URL, "/v1/me")
	if len(me) != 1 || me[0].Status != 404 || me[0].ProofTarget != f.api.URL+"/v1/me" {
		t.Errorf("API host /v1/me hits = %+v, want exactly one 404 with a proof for the API origin", me)
	}
	if hits := f.hitsAt(f.srv.URL, "/v1/me"); len(hits) != 1 || hits[0].Status != 200 || hits[0].ProofTarget != f.srv.URL+"/v1/me" {
		t.Errorf("issuer /v1/me hits = %+v, want one 200 with a proof for the issuer origin", hits)
	}
	if hits := f.hitsAt(f.api.URL, "/user/accounts"); len(hits) != 0 {
		t.Errorf("API host /user/accounts hits = %+v; the run already learned that the issuer serves it", hits)
	}
	if hits := f.hitsAt(f.srv.URL, "/user/accounts"); len(hits) != 1 || hits[0].Status != 200 || hits[0].ProofTarget != f.srv.URL+"/user/accounts" {
		t.Errorf("issuer /user/accounts hits = %+v, want one 200 with a proof for the issuer origin", hits)
	}
	// Reading the identity again later in the run stays on the issuer.
	if _, err := s.Identity(); err != nil {
		t.Fatal(err)
	}
	if hits := f.hitsAt(f.api.URL, "/v1/me"); len(hits) != 1 {
		t.Errorf("API host /v1/me was tried %d times in the run, want 1", len(hits))
	}
}

// The device poll and the refresh belong to the issuer on every shape: the API host never sees them.
func TestDeviceAndRefreshStayOnTheIssuer(t *testing.T) {
	for name, split := range map[string]func(*fakeRoid){"prod": (*fakeRoid).splitAPIHost, "dev": (*fakeRoid).splitDevHosts} {
		t.Run(name, func(t *testing.T) {
			f := newFakeRoid(t)
			split(f)
			s := loginSession(t, f)
			login(t, f, s)
			s.mu.Lock()
			s.accessExp = s.now() // force a refresh
			s.mu.Unlock()
			if _, err := s.Bearer(); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"/device/", "/cli/"} {
				if hits := f.hitsAt(f.api.URL, route); len(hits) != 0 {
					t.Errorf("the API host got %+v for %s", hits, route)
				}
			}
			if f.refreshes != 1 || len(f.hitsAt(f.srv.URL, "/device/token")) == 0 {
				t.Errorf("refreshes = %d, device polls on the issuer = %d", f.refreshes, len(f.hitsAt(f.srv.URL, "/device/token")))
			}
		})
	}
}

// A 401 on the first host is the answer: the access token is not sent to the other host after it.
func TestAnUnauthorizedIdentityReadIsNotRetried(t *testing.T) {
	f := newFakeRoid(t)
	f.splitAPIHost()
	s := loginSession(t, f)
	key, _ := NewKey()
	if _, err := s.Client.identity("at_not_a_token_of_this_fake", key); err == nil {
		t.Fatal("a bad token read an identity")
	}
	if hits := f.hitsAt(f.srv.URL, "/v1/me"); len(hits) != 0 {
		t.Errorf("a 401 on the API host sent the token to the issuer: %+v", hits)
	}
}
