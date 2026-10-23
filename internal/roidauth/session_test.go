package roidauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/securestore"
)

// memStore is an in-memory secure store for tests.
type memStore struct {
	mu    sync.Mutex
	items map[string][]byte
}

func newMemStore() *memStore { return &memStore{items: map[string][]byte{}} }

func (m *memStore) Get(item string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[item]
	if !ok {
		return nil, securestore.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m *memStore) Set(item string, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item] = append([]byte(nil), secret...)
	return nil
}

func (m *memStore) Delete(item string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[item]; !ok {
		return securestore.ErrNotFound
	}
	delete(m.items, item)
	return nil
}

func (m *memStore) Kind() string { return "memory" }

func newTestSession(t *testing.T, f *fakeRoid, store securestore.Store) *Session {
	c := NewClient(f.srv.URL)
	c.Sleep = noSleep
	return &Session{Issuer: f.srv.URL, Store: store, LockPath: filepath.Join(t.TempDir(), "roid.lock"), Client: c}
}

func login(t *testing.T, f *fakeRoid, s *Session) Identity {
	t.Helper()
	f.approve()
	var shown DeviceStart
	id, err := s.Login(context.Background(), func(d DeviceStart) { shown = d })
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if shown.UserCode == "" {
		t.Fatal("login never showed the user code")
	}
	return id
}

func TestLoginReadsIdentityFromTheAPIHostWhenTheHostsAreSplit(t *testing.T) {
	f := newFakeRoid(t)
	f.splitAPIHost()
	c := NewClientWithAPI(f.srv.URL, f.api.URL)
	c.Sleep = noSleep
	s := &Session{Issuer: f.srv.URL, Store: newMemStore(), LockPath: filepath.Join(t.TempDir(), "roid.lock"), Client: c}
	id := login(t, f, s)
	if id.PersonID != "p_alice" || len(id.Accounts) != 1 {
		t.Fatalf("identity = %+v", id)
	}
}

func TestLoginKeepsEverySecretInTheStore(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	id := login(t, f, s)
	if id.PersonID != "p_alice" || len(id.Accounts) != 1 || id.Accounts[0] != "acct1" {
		t.Fatalf("identity = %+v", id)
	}
	if s.Person != "p_alice" {
		t.Fatalf("session person %q", s.Person)
	}
	if len(store.items) != 2 {
		t.Fatalf("store holds %d items, want the refresh token and the key seed", len(store.items))
	}
	// Nothing secret may land on disk beside the lock.
	entries, _ := os.ReadDir(filepath.Dir(s.LockPath))
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(s.LockPath), e.Name()))
		for _, v := range store.items {
			if len(v) > 0 && bytes.Contains(raw, v) {
				t.Fatalf("%s holds a stored secret", e.Name())
			}
		}
	}
}

func TestBearerRotatesOnceAndReusesTheAccessTokenInTheProcess(t *testing.T) {
	f := newFakeRoid(t)
	s := newTestSession(t, f, newMemStore())
	login(t, f, s)
	a, err := s.Bearer()
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Bearer()
	if err != nil {
		t.Fatal(err)
	}
	if a != b || !strings.HasPrefix(a, "at_") {
		t.Fatalf("bearer %q then %q, want the same cached access token", a, b)
	}
	// Login already holds a fresh access token, so no refresh was needed.
	if f.refreshes != 0 {
		t.Fatalf("%d refreshes, want 0", f.refreshes)
	}
}

func TestANewProcessRefreshesAndStoresTheRotatedToken(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	login(t, f, s)
	before := string(store.items[s.refreshItem()])

	// A second process: same store and person, nothing cached.
	s2 := newTestSession(t, f, store)
	s2.Person = s.Person
	tok, err := s2.Bearer()
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if !strings.HasPrefix(tok, "at_") || f.refreshes != 1 {
		t.Fatalf("token %q after %d refreshes", tok, f.refreshes)
	}
	if after := string(store.items[s.refreshItem()]); after == before || after == "" {
		t.Fatal("the rotated refresh token was not stored")
	}
}

func TestAReplayedRefreshTokenEndsTheSessionWithAPlainMessage(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	login(t, f, s)
	stolen := append([]byte(nil), store.items[s.refreshItem()]...)

	s2 := newTestSession(t, f, store)
	s2.Person = s.Person
	if _, err := s2.Bearer(); err != nil {
		t.Fatal(err)
	}
	// Someone presents the spent token: roid revokes the whole family.
	store.items[s.refreshItem()] = stolen
	s3 := newTestSession(t, f, store)
	s3.Person = s.Person
	_, err := s3.Bearer()
	if !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("err = %v, want ErrSessionEnded", err)
	}
	if !strings.Contains(err.Error(), "runos login") {
		t.Fatalf("error %q does not say how to fix it", err)
	}
}

func TestLogoutRevokesOnTheServerAndDeletesLocalSecrets(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	login(t, f, s)
	if err := s.Logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if f.logouts != 1 || len(store.items) != 0 {
		t.Fatalf("logouts %d, store items %d", f.logouts, len(store.items))
	}
	if _, err := s.Bearer(); !errors.Is(err, ErrSessionEnded) && !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("bearer after logout: %v", err)
	}
}

func TestLogoutDeletesLocalSecretsWhenTheServerIsDown(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	login(t, f, s)
	f.srv.Close()
	err := s.Logout()
	if err == nil || !strings.Contains(err.Error(), "server") {
		t.Fatalf("logout with the server down: %v; want an error naming the server", err)
	}
	if len(store.items) != 0 {
		t.Fatal("local secrets survived a logout")
	}
}

func TestProofTransportSignsOnlyThisSessionsTokenToAllowedHosts(t *testing.T) {
	f := newFakeRoid(t)
	s := newTestSession(t, f, newMemStore())
	login(t, f, s)
	at, err := s.Bearer()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = append(got, r.Header.Get(ProofHeader)) }))
	defer api.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = append(got, r.Header.Get(ProofHeader)) }))
	defer other.Close()
	reg := &registry{}
	apiURL, _ := url.Parse(api.URL)
	reg.add(at, s.key, time.Now().Add(time.Minute), apiURL.Host)
	client := &http.Client{Transport: &proofTransport{base: http.DefaultTransport, reg: reg}}

	send := func(target, token string) {
		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	send(api.URL, at)
	send(api.URL, at)
	send(other.URL, at)
	send(api.URL, "at_someone_else")
	if got[0] == "" || got[1] == "" || got[0] == got[1] {
		t.Fatalf("allowed host got proofs %q and %q; want two different proofs", got[0], got[1])
	}
	if got[2] != "" || got[3] != "" {
		t.Fatalf("proof sent to another host (%q) or for another token (%q)", got[2], got[3])
	}
}

func TestLogoutStopsTheTransportProvingTheOldToken(t *testing.T) {
	f := newFakeRoid(t)
	s := newTestSession(t, f, newMemStore())
	s.ProofHosts = []string{"api.example.com"}
	login(t, f, s)
	at, _ := s.Bearer()
	if _, ok := defaultRegistry.lookup(at, "api.example.com"); !ok {
		t.Fatal("the access token was never registered with the proof transport")
	}
	if err := s.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, ok := defaultRegistry.lookup(at, "api.example.com"); ok {
		t.Fatal("the proof transport still proves a logged-out token")
	}
}

// FPL321 c5: a proof the transport makes for an API request names that
// request, so the API server cannot spend it at roid, and it carries the
// method and the URL without the query.
func TestAProofMadeForTheAPICannotBeSpentAtRoid(t *testing.T) {
	f := newFakeRoid(t)
	s := newTestSession(t, f, newMemStore())
	login(t, f, s)
	at, err := s.Bearer()
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Get(ProofHeader) }))
	defer api.Close()
	reg := &registry{}
	apiURL, _ := url.Parse(api.URL)
	reg.add(at, s.key, time.Now().Add(time.Minute), apiURL.Host)
	client := &http.Client{Transport: &proofTransport{base: http.DefaultTransport, reg: reg}}
	req, _ := http.NewRequest("GET", api.URL+"/v1/apps?page=2#x", nil)
	req.Header.Set("Authorization", "Bearer "+at)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	raw, _ := base64.RawURLEncoding.DecodeString(seen)
	var p wireProof
	if err := json.Unmarshal(raw, &p); err != nil || p.Method != "GET" || p.Target != api.URL+"/v1/apps" {
		t.Fatalf("the API proof names %q %q (%v); want GET %s/v1/apps", p.Method, p.Target, err, api.URL)
	}
	// The API server replays it at roid: refused.
	replay, _ := http.NewRequest("GET", f.srv.URL+"/v1/me", nil)
	replay.Header.Set("Authorization", "Bearer "+at)
	replay.Header.Set(ProofHeader, seen)
	rr, err := http.DefaultTransport.RoundTrip(replay)
	if err != nil {
		t.Fatal(err)
	}
	rr.Body.Close()
	if rr.StatusCode == 200 {
		t.Fatal("roid accepted a proof the CLI made for the API")
	}
}
