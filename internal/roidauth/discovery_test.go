package roidauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// discoveryServer serves one discovery answer on the well-known path. The
// issuer and device endpoint fill in from the server's own URL.
func discoveryServer(t *testing.T, status int, doc func(base string) any) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DiscoveryPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if doc != nil {
			_ = json.NewEncoder(w).Encode(doc(srv.URL))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func goodDoc(base string) any {
	return map[string]any{"issuer": base, "device_authorization_endpoint": base + "/device/authorize", "jwks_uri": base + "/jwks"}
}

func TestCheckIssuerAcceptsAValidDiscoveryDocument(t *testing.T) {
	srv := discoveryServer(t, 200, goodDoc)
	if err := CheckIssuer(srv.URL, time.Second); err != nil {
		t.Fatalf("a valid discovery document was refused: %v", err)
	}
	// A trailing slash on the configured issuer names the same issuer.
	if err := CheckIssuer(srv.URL+"/", time.Second); err != nil {
		t.Fatalf("trailing slash: %v", err)
	}
}

func TestCheckIssuerRefusesAnIssuerThatDoesNotAnswerADiscoveryDocument(t *testing.T) {
	notFound := httptest.NewServer(http.NotFoundHandler()) // the prod shape: the host answers 404
	t.Cleanup(notFound.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close() // nothing listens: connection refused

	cases := []struct {
		name   string
		issuer string
		want   string
	}{
		{"404 on the discovery path", notFound.URL, "404"},
		{"nothing listens", closedURL, "reach"},
		{"server error", discoveryServer(t, 503, goodDoc).URL, "503"},
		{"not json", discoveryServer(t, 200, func(string) any { return "<html>" }).URL, "discovery"},
		{"empty object", discoveryServer(t, 200, func(string) any { return map[string]any{} }).URL, "issuer"},
		{"names another issuer", discoveryServer(t, 200, func(base string) any {
			return map[string]any{"issuer": "https://other.example.com", "device_authorization_endpoint": base + "/device/authorize"}
		}).URL, "issuer"},
		{"no device endpoint", discoveryServer(t, 200, func(base string) any { return map[string]any{"issuer": base} }).URL, "device"},
		{"device endpoint elsewhere", discoveryServer(t, 200, func(base string) any {
			return map[string]any{"issuer": base, "device_authorization_endpoint": "https://other.example.com/device/authorize"}
		}).URL, "device"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckIssuer(tc.issuer, time.Second)
			if err == nil {
				t.Fatalf("CheckIssuer accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestCheckIssuerDoesNotFollowARedirect(t *testing.T) {
	target := discoveryServer(t, 200, goodDoc)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+DiscoveryPath, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	if err := CheckIssuer(redirector.URL, time.Second); err == nil {
		t.Fatal("a redirect to another host's discovery document was accepted")
	}
}

func TestCheckIssuerGivesUpWithinItsDeadline(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); slow.Close() })
	start := time.Now()
	if err := CheckIssuer(slow.URL, 200*time.Millisecond); err == nil {
		t.Fatal("a hung issuer was accepted")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the check took %s against a 200ms deadline", took)
	}
}
