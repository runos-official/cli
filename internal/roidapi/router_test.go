package roidapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/api"
	"github.com/runos-official/cli/internal/config"
)

// stubHost is a host that answers every request with one fixed answer and counts what it saw.
type stubHost struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
	auths []string
}

func newStubHost(t *testing.T, status int, body string) *stubHost {
	t.Helper()
	h := &stubHost{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.calls = append(h.calls, r.Method+" "+r.URL.Path)
		h.auths = append(h.auths, r.Header.Get("Authorization"))
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *stubHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

const (
	notOnThisHost = `{"error":{"code":"not_found","reason":"not_on_this_host"}}`
	conductor404  = `{"error":"Not found"}`
)

// splitProd is the PROD shape: the gateway (API host, rapi) serves the access API and the issuer
// answers 404 not_on_this_host for it.
func splitProd(t *testing.T) (router *Router, gateway *httptest.Server, issuer *stubHost, fake *fakeRoid) {
	t.Helper()
	fake, gateway = newFakeRoid(t)
	issuer = newStubHost(t, 404, notOnThisHost)
	return NewRouter(gateway.URL, issuer.srv.URL), gateway, issuer, fake
}

// splitDev is the DEV shape: the API host goes to conductor, which answers 404 for the access routes,
// and the issuer serves every route.
func splitDev(t *testing.T) (router *Router, gateway *stubHost, issuer *httptest.Server, fake *fakeRoid) {
	t.Helper()
	fake, issuer = newFakeRoid(t)
	gateway = newStubHost(t, 404, conductor404)
	return NewRouter(gateway.srv.URL, issuer.URL), gateway, issuer, fake
}

func listRoles(c *Client) (*api.Result, error) {
	return c.Do(Call{Op: "listRoles", Params: map[string]string{"acct": "a1"}})
}

func TestProdShapeAccessCallGoesToTheGatewayWithNoRetry(t *testing.T) {
	r, _, issuer, fake := splitProd(t)
	res, err := listRoles(NewRouted(r, staticToken("at_test")))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("listRoles: %v %v", res, err)
	}
	if len(fake.calls) != 1 || issuer.count() != 0 {
		t.Errorf("gateway calls = %v, issuer calls = %d; want one call to the gateway and none to the issuer", fake.calls, issuer.count())
	}
}

func TestDevShapeAccessCallRetriesOnTheIssuerExactlyOnce(t *testing.T) {
	r, gateway, _, fake := splitDev(t)
	res, err := listRoles(NewRouted(r, staticToken("at_test")))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("listRoles: %v %v", res, err)
	}
	if gateway.count() != 1 || len(fake.calls) != 1 {
		t.Errorf("gateway calls = %d, issuer calls = %v; want exactly one try on each", gateway.count(), fake.calls)
	}
}

func TestTheHostThatWorkedIsRememberedForTheRestOfTheRun(t *testing.T) {
	r, gateway, _, fake := splitDev(t)
	c := NewRouted(r, staticToken("at_test"))
	for i := 0; i < 3; i++ {
		if res, err := listRoles(c); err != nil || res.StatusCode != 200 {
			t.Fatalf("call %d: %v %v", i, res, err)
		}
	}
	if gateway.count() != 1 {
		t.Errorf("the gateway was tried %d times; after the first 404 the run goes straight to the issuer", gateway.count())
	}
	if len(fake.calls) != 3 {
		t.Errorf("issuer calls = %v, want 3", fake.calls)
	}
	// A second client for the same two hosts in the same run starts from what the first learned.
	if _, err := listRoles(NewRouted(NewRouter(gateway.srv.URL, r.issuer), staticToken("at_test"))); err != nil {
		t.Fatal(err)
	}
	if gateway.count() != 1 {
		t.Errorf("a new router for the same hosts tried the gateway again (%d tries)", gateway.count())
	}
}

func TestAWorkingGatewayIsRememberedToo(t *testing.T) {
	r, _, issuer, fake := splitProd(t)
	c := NewRouted(r, staticToken("at_test"))
	for i := 0; i < 3; i++ {
		if _, err := listRoles(c); err != nil {
			t.Fatal(err)
		}
	}
	if issuer.count() != 0 || len(fake.calls) != 3 {
		t.Errorf("issuer calls = %d, gateway calls = %v", issuer.count(), fake.calls)
	}
}

// A write whose first host answered 404 did nothing there, so the same request is sent once to the
// other host, with its body, its If-Match and its step-up header intact.
func TestTheRetryResendsTheSameRequest(t *testing.T) {
	r, _, _, fake := splitDev(t)
	fake.roles["ops"] = &fakeVersioned{version: 3, body: map[string]any{"id": "ops", "name": "Ops", "builtin": false, "actions": []string{"clusters.read"}}}
	c := NewRouted(r, staticToken("at_test"))
	res, err := c.Do(Call{Op: "updateRole", Params: map[string]string{"acct": "a1", "role": "ops"},
		Body: map[string]any{"name": "Operators", "actions": []string{"clusters.read"}}, StepUp: "su_x"})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("updateRole: %v %v", res, err)
	}
	if fake.roles["ops"].body["name"] != "Operators" {
		t.Errorf("the body did not reach the issuer: %v", fake.roles["ops"].body)
	}
	last := fake.headers[len(fake.headers)-1]
	if last.Get("If-Match") != `"v3"` || last.Get("X-RunOS-Step-Up") != "su_x" || last.Get("Authorization") != "Bearer at_test" {
		t.Errorf("the issuer got headers %v", last)
	}
}

// Only a 404 moves a request to the other host. Any other answer is the answer.
func TestOnlyA404IsRetried(t *testing.T) {
	for _, status := range []int{200, 201, 204, 400, 401, 403, 409, 412, 428, 429, 500, 502, 503} {
		first := newStubHost(t, status, `{"error":{"code":"x"}}`)
		second := newStubHost(t, 200, `{}`)
		r := NewRouter(first.srv.URL, second.srv.URL)
		res, err := r.Send(api.Request{Method: "POST", Path: "/v1/accounts/a1/access/roles", Token: "at_x", JSON: map[string]any{"id": "r"}})
		if err != nil || res.StatusCode != status {
			t.Errorf("status %d: result = %v err = %v", status, res, err)
		}
		if first.count() != 1 || second.count() != 0 {
			t.Errorf("status %d: first host %d calls, second host %d calls; only a 404 may be retried", status, first.count(), second.count())
		}
	}
}

func TestNoRetryAfterAConnectionFailure(t *testing.T) {
	first := newStubHost(t, 200, `{}`)
	second := newStubHost(t, 200, `{}`)
	url := first.srv.URL
	first.srv.Close()
	r := NewRouter(url, second.srv.URL)
	if _, err := r.Send(api.Request{Method: "GET", Path: "/v1/me", Token: "at_x"}); err == nil {
		t.Fatal("a closed host gave no error")
	}
	if second.count() != 0 {
		t.Errorf("the second host got %d calls after a connection failure on the first", second.count())
	}
}

func TestOneHostMeansNoRetry(t *testing.T) {
	only := newStubHost(t, 404, conductor404)
	for _, issuer := range []string{only.srv.URL, only.srv.URL + "/"} {
		r := NewRouter(only.srv.URL, issuer)
		res, err := r.Send(api.Request{Method: "GET", Path: "/v1/me", Token: "at_x"})
		if err != nil || res.StatusCode != 404 {
			t.Fatalf("result = %v err = %v", res, err)
		}
	}
	if only.count() != 2 {
		t.Errorf("calls = %d, want 2 (one per send, never a retry on the same host)", only.count())
	}
}

// A genuine not-found on the gateway (roid knows the route, the role does not exist) is answered by
// the issuer with not_on_this_host. The person must see the gateway's answer, not that one.
func TestAGenuineNotFoundKeepsTheGatewaysAnswerOnProd(t *testing.T) {
	r, _, issuer, _ := splitProd(t)
	_, err := NewRouted(r, staticToken("at_test")).Do(Call{Op: "getGrant", Params: map[string]string{"acct": "a1", "grant": "nope"}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" || ae.Reason == "not_on_this_host" {
		t.Fatalf("err = %v, want the gateway's own not_found", err)
	}
	if issuer.count() != 1 {
		t.Errorf("issuer calls = %d, want the one retry", issuer.count())
	}
}

// On the dev shape a genuine not-found comes from the issuer, the host that knows the route.
func TestAGenuineNotFoundComesFromTheIssuerOnDev(t *testing.T) {
	r, _, _, _ := splitDev(t)
	_, err := NewRouted(r, staticToken("at_test")).Do(Call{Op: "getGrant", Params: map[string]string{"acct": "a1", "grant": "nope"}})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 404 || ae.Code != "not_found" {
		t.Fatalf("err = %v, want the issuer's not_found", err)
	}
}

// The second host comes from the configuration, never from an answer. A 404 that names another
// address (a redirect, a body, a header) must not make the CLI send the bearer there.
func TestA404CannotNameWhereTheCredentialsGo(t *testing.T) {
	evil := newStubHost(t, 200, `{}`)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", evil.srv.URL+r.URL.Path)
		w.Header().Set("X-RunOS-Retry-At", evil.srv.URL)
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","reason":"not_on_this_host","host":"` + evil.srv.URL + `","url":"` + evil.srv.URL + `"}}`))
	}))
	t.Cleanup(first.Close)
	issuer := newStubHost(t, 404, notOnThisHost)
	r := NewRouter(first.URL, issuer.srv.URL)
	res, err := r.Send(api.Request{Method: "GET", Path: "/v1/accounts/a1/access/roles", Token: "at_secret"})
	if err != nil || res.StatusCode != 404 {
		t.Fatalf("result = %v err = %v", res, err)
	}
	if evil.count() != 0 {
		t.Fatalf("a host named only by a 404 got %d requests with the bearer", evil.count())
	}
	if issuer.count() != 1 {
		t.Errorf("the configured issuer got %d requests, want 1", issuer.count())
	}
}

func TestRouterForTakesBothHostsFromTheConfig(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	cfg := &config.Config{AuthIssuer: "https://id.example.com/", ConductorURL: "https://api.example.com/"}
	r, err := RouterFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.gateway != "https://api.example.com" || r.issuer != "https://id.example.com" {
		t.Errorf("gateway = %q issuer = %q", r.gateway, r.issuer)
	}
	if _, err := RouterFor(&config.Config{}); !errors.Is(err, ErrNotRoid) {
		t.Errorf("a Firebase environment: err = %v, want ErrNotRoid", err)
	}
}

// RUNOS_API_URL names the first host to try. When it is the issuer itself there is one host.
func TestTheOverrideNamesTheFirstHost(t *testing.T) {
	cfg := &config.Config{AuthIssuer: "https://id.example.com", ConductorURL: "https://api.example.com"}
	t.Setenv("RUNOS_API_URL", "https://id.example.com/")
	r, err := RouterFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !r.single() {
		t.Errorf("override = the issuer: gateway %q issuer %q should be one host", r.gateway, r.issuer)
	}
	t.Setenv("RUNOS_API_URL", "http://localhost:9999")
	r, _ = RouterFor(cfg)
	if r.gateway != "http://localhost:9999" || r.issuer != "https://id.example.com" {
		t.Errorf("gateway = %q issuer = %q", r.gateway, r.issuer)
	}
}

// A request whose headers name the host (a proof) gets them built once per host it goes to, for that
// host's own address, and the request's other headers stay.
func TestSendForBuildsTheHeaderForEachHostItGoesTo(t *testing.T) {
	gotHeader := map[string]string{}
	var mu sync.Mutex
	serve := func(status int) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			gotHeader[r.Host] = r.Header.Get("X-RunOS-Proof") + "|" + r.Header.Get("X-Keep")
			mu.Unlock()
			w.WriteHeader(status)
		}))
		t.Cleanup(s.Close)
		return s
	}
	gw, is := serve(404), serve(200)
	var origins []string
	res, err := NewRouter(gw.URL, is.URL).SendFor(
		api.Request{Method: "GET", Path: "/v1/me", Token: "at_x", Header: http.Header{"X-Keep": {"yes"}}},
		func(origin string) (http.Header, error) {
			origins = append(origins, origin)
			return http.Header{"X-Runos-Proof": {"proof-for-" + origin}}, nil
		})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("result = %v err = %v", res, err)
	}
	if len(origins) != 2 || origins[0] != gw.URL || origins[1] != is.URL {
		t.Fatalf("origins asked = %v, want the gateway then the issuer", origins)
	}
	for _, s := range []*httptest.Server{gw, is} {
		host := s.Listener.Addr().String()
		if want := "proof-for-" + s.URL + "|yes"; gotHeader[host] != want {
			t.Errorf("host %s got %q, want %q", host, gotHeader[host], want)
		}
	}
}
