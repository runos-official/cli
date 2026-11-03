package kubeaccess

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeRoid answers the token route the way roid's contract says
// (docs/work/fpl365-k8s-routes.md R1) and records what it saw.
type fakeRoid struct {
	mu       sync.Mutex
	calls    int
	method   string
	path     string
	auth     string
	bodyLen  int
	status   int
	respBody string
	headers  map[string]string
}

func (f *fakeRoid) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.method, f.path, f.auth = r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization")
	b, _ := io.ReadAll(r.Body)
	f.bodyLen = len(b)
	for k, v := range f.headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	if f.status == 0 {
		f.status = 200
	}
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.respBody)
}

func okBody(exp time.Time) string {
	b, _ := json.Marshal(map[string]any{
		"token": "jwt.header.sig", "expires_at": exp.UTC().Format(time.RFC3339), "expires_in": 300,
		"issuer": "https://id.example.com/k8s", "audience": "runos-k8s:cluster-a1",
		"subject": "runos:u:person-a", "groups": []string{"runos:cw:view"}, "jti": "j1",
	})
	return string(b)
}

func newFetcher(srv *httptest.Server) *Fetcher {
	return &Fetcher{BaseURL: srv.URL, Account: "acct-1", Cluster: "cl-1", Bearer: func() (string, error) { return "at_test", nil }}
}

func TestFetchPostsToTheTokenRouteWithTheSessionBearer(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute)
	f := &fakeRoid{respBody: okBody(exp)}
	srv := httptest.NewServer(f)
	defer srv.Close()

	got, err := newFetcher(srv).Fetch()
	if err != nil {
		t.Fatal(err)
	}
	if f.method != "POST" || f.path != "/v1/accounts/acct-1/clusters/cl-1/k8s/token" {
		t.Errorf("called %s %s", f.method, f.path)
	}
	if f.auth != "Bearer at_test" {
		t.Errorf("Authorization = %q", f.auth)
	}
	if f.bodyLen != 0 {
		t.Errorf("the token route takes no body, sent %d bytes", f.bodyLen)
	}
	if got.Token != "jwt.header.sig" || !got.ExpiresAt.Equal(exp.UTC().Truncate(time.Second)) {
		t.Errorf("issued = %+v", got)
	}
}

func TestFetchEscapesIDsInThePath(t *testing.T) {
	f := &fakeRoid{respBody: okBody(time.Now().Add(time.Minute))}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ft := newFetcher(srv)
	ft.Account, ft.Cluster = "a/b", "c d"
	if _, err := ft.Fetch(); err != nil {
		t.Fatal(err)
	}
	if f.path != "/v1/accounts/a%2Fb/clusters/c%20d/k8s/token" {
		t.Errorf("path = %s", f.path)
	}
}

func TestFetchRefusesAnAnswerWithoutAUsableToken(t *testing.T) {
	for name, body := range map[string]string{
		"no token":       `{"expires_at":"2099-01-01T00:00:00Z"}`,
		"no expiry":      `{"token":"x"}`,
		"bad expiry":     `{"token":"x","expires_at":"soon"}`,
		"not json":       `<html>gateway</html>`,
		"empty response": ``,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(&fakeRoid{respBody: body})
			defer srv.Close()
			if _, err := newFetcher(srv).Fetch(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestFetchReturnsASessionErrorFromTheBearerUnchanged(t *testing.T) {
	boom := errors.New("not signed in")
	ft := &Fetcher{BaseURL: "http://127.0.0.1:1", Account: "a", Cluster: "c", Bearer: func() (string, error) { return "", boom }}
	if _, err := ft.Fetch(); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchReturnsARefusalWithRoidsWords(t *testing.T) {
	srv := httptest.NewServer(&fakeRoid{status: 403, respBody: `{"error":{"code":"authz.denied","reason":"deny_listed","message":"Your access ended at 10:00."}}`})
	defer srv.Close()
	_, err := newFetcher(srv).Fetch()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v", err)
	}
	if r.Status != 403 || r.Code != "authz.denied" || r.Reason != "deny_listed" || r.Message != "Your access ended at 10:00." {
		t.Errorf("refusal = %+v", r)
	}
}

func TestSourceReusesATokenUntilThirtySecondsBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	f := &fakeRoid{respBody: okBody(now.Add(300 * time.Second))}
	srv := httptest.NewServer(f)
	defer srv.Close()
	src := &Source{Fetcher: newFetcher(srv), Now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if _, err := src.Token(); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", f.calls)
	}
	now = now.Add(269 * time.Second) // 31 seconds left
	if _, err := src.Token(); err != nil || f.calls != 1 {
		t.Fatalf("31s left: calls = %d err = %v", f.calls, err)
	}
	now = now.Add(2 * time.Second) // 29 seconds left
	f.respBody = okBody(now.Add(300 * time.Second))
	if _, err := src.Token(); err != nil || f.calls != 2 {
		t.Fatalf("29s left: calls = %d err = %v", f.calls, err)
	}
}

func TestSourceDoesNotCacheARefusal(t *testing.T) {
	f := &fakeRoid{status: 503, respBody: `{"error":{"code":"signer_unavailable"}}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	src := &Source{Fetcher: newFetcher(srv), Now: time.Now}
	if _, err := src.Token(); err == nil {
		t.Fatal("want an error")
	}
	f.status, f.respBody = 200, okBody(time.Now().Add(time.Minute))
	if _, err := src.Token(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if f.calls != 2 {
		t.Fatalf("calls = %d", f.calls)
	}
}
