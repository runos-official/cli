package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidapi"
)

// hostLog is one fake host: it records the requests it saw and answers with serve.
type hostLog struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
}

func (h *hostLog) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

func newHost(t *testing.T, serve func(w http.ResponseWriter, r *http.Request)) *hostLog {
	t.Helper()
	h := &hostLog{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.calls = append(h.calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		serve(w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// rapiRoutes answers the two rapi-only routes the CLI uses in these tests.
func rapiRoutes(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/access/roles"):
		_, _ = w.Write([]byte(`{"items":[{"id":"viewer","name":"Viewer"}]}`))
	case strings.HasSuffix(r.URL.Path, "/k8s/token"):
		exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"token":"jwt.a.b","expires_at":"` + exp + `"}`))
	default:
		w.WriteHeader(404)
	}
}

func prodHosts(t *testing.T) (issuer, gateway *hostLog) {
	issuer = newHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","reason":"not_on_this_host"}}`))
	})
	return issuer, newHost(t, rapiRoutes)
}

func devHosts(t *testing.T) (issuer, gateway *hostLog) {
	gateway = newHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"Not found"}`))
	})
	return newHost(t, rapiRoutes), gateway
}

func hostConfig(issuer, gateway *hostLog) *config.Config {
	c := roidConfig()
	c.AuthIssuer, c.Roid.Issuer, c.ConductorURL = issuer.srv.URL, issuer.srv.URL, gateway.srv.URL
	return c
}

func accessRolesCall(t *testing.T, cfg *config.Config) error {
	t.Helper()
	client, err := accessClient(cfg, func() (string, error) { return "at_x", nil })
	if err != nil {
		t.Fatal(err)
	}
	tg := &accessTarget{client: client, acct: "acct-1"}
	_, err = tg.do(roidapi.Call{Op: "listRoles"})
	return err
}

// The same binary and the same config shape work on both environments with no RUNOS_API_URL.
func TestAccessCommandsWorkOnTheProdShapeWithNoOverride(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer, gateway := prodHosts(t)
	if err := accessRolesCall(t, hostConfig(issuer, gateway)); err != nil {
		t.Fatal(err)
	}
	if len(gateway.seen()) != 1 || len(issuer.seen()) != 0 {
		t.Errorf("gateway %v issuer %v", gateway.seen(), issuer.seen())
	}
}

func TestAccessCommandsWorkOnTheDevShapeWithNoOverride(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer, gateway := devHosts(t)
	if err := accessRolesCall(t, hostConfig(issuer, gateway)); err != nil {
		t.Fatal(err)
	}
	if len(gateway.seen()) != 1 || len(issuer.seen()) != 1 {
		t.Errorf("gateway %v issuer %v; want one try on each", gateway.seen(), issuer.seen())
	}
}

func TestTokenCallWorksOnTheDevShapeWithNoOverrideAndRemembersTheHost(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer, gateway := devHosts(t)
	cfg := hostConfig(issuer, gateway)
	for i := 0; i < 2; i++ {
		iss, err := fetchWith(t, cfg)
		if err != nil || iss.Token != "jwt.a.b" {
			t.Fatalf("call %d: token = %+v err = %v", i, iss, err)
		}
	}
	if len(gateway.seen()) != 1 || len(issuer.seen()) != 2 {
		t.Errorf("gateway %v issuer %v; the gateway is tried once, then the run stays on the issuer", gateway.seen(), issuer.seen())
	}
}

func TestTokenCallWorksOnTheProdShapeWithNoOverride(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer, gateway := prodHosts(t)
	if _, err := fetchWith(t, hostConfig(issuer, gateway)); err != nil {
		t.Fatal(err)
	}
	if len(gateway.seen()) != 1 || len(issuer.seen()) != 0 {
		t.Errorf("gateway %v issuer %v", gateway.seen(), issuer.seen())
	}
}

// A refusal from the first host is the answer: the token call is not sent to the issuer after a 403.
func TestTokenRefusalIsNotRetriedOnTheOtherHost(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer := newHost(t, rapiRoutes)
	gateway := newHost(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"authz.denied","reason":"no_grant"}}`))
	})
	if _, err := fetchWith(t, hostConfig(issuer, gateway)); err == nil {
		t.Fatal("a 403 became a token")
	}
	if len(issuer.seen()) != 0 {
		t.Errorf("the issuer got %v after the gateway refused", issuer.seen())
	}
}
