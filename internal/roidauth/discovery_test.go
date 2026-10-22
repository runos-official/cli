package roidauth

import (
	"encoding/json"
	"errors"
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

// The redirect target serves a document that is valid FOR THE REDIRECTOR's issuer, so only the
// refusal to follow a redirect stands between the check and a pass. With CheckRedirect removed this
// test fails.
func TestCheckIssuerDoesNotFollowARedirect(t *testing.T) {
	var redirectorURL string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(goodDoc(redirectorURL))
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+DiscoveryPath, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	redirectorURL = redirector.URL

	err := CheckIssuer(redirector.URL, time.Second)
	if err == nil {
		t.Fatal("a redirect to another host's discovery document was accepted")
	}
	var nd *IssuerNotDeployedError
	if errors.As(err, &nd) || !strings.Contains(err.Error(), "307") {
		t.Fatalf("error %q: a redirect is a bad answer, not a not-deployed answer", err)
	}
}

// The response size is capped (internal/api, 4 MiB). A document that is valid JSON but larger than
// the cap is refused instead of read into memory.
func TestCheckIssuerRefusesAnOversizedDocument(t *testing.T) {
	big := discoveryServer(t, 200, func(base string) any {
		doc := goodDoc(base).(map[string]any)
		doc["padding"] = strings.Repeat("A", 5<<20)
		return doc
	})
	if err := CheckIssuer(big.URL, 10*time.Second); err == nil {
		t.Fatal("a 5 MiB discovery document was accepted; the response cap is gone")
	}
}

// Only a definitive "not deployed" answer is typed as such. The caller falls back to Firebase on
// that and on nothing else.
func TestCheckIssuerTypesOnlyA404Or410AsNotDeployed(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close() // TCP close with no answer
	}))
	t.Cleanup(reset.Close)
	tlsUntrusted := httptest.NewTLSServer(http.NotFoundHandler()) // 404 behind a certificate this machine does not trust
	t.Cleanup(tlsUntrusted.Close)
	status := func(code int) string {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })).URL
	}

	for _, tc := range []struct {
		name        string
		issuer      string
		notDeployed bool
		wantInErr   string
	}{
		{"404", status(404), true, "404"},
		{"410", status(410), true, "410"},
		{"500", status(500), false, "500"},
		{"502", status(502), false, "502"},
		{"503", status(503), false, "503"},
		{"401", status(401), false, "401"},
		{"403", status(403), false, "403"},
		{"302", status(302), false, "302"},
		{"connection refused", closedURL, false, "reach"},
		{"connection reset", reset.URL, false, "reach"},
		{"404 behind an untrusted certificate", tlsUntrusted.URL, false, "reach"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckIssuer(tc.issuer, 2*time.Second)
			if err == nil {
				t.Fatal("accepted")
			}
			var nd *IssuerNotDeployedError
			if got := errors.As(err, &nd); got != tc.notDeployed {
				t.Fatalf("NotDeployed = %v, want %v (%v)", got, tc.notDeployed, err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantInErr)
			}
		})
	}
}

// A timeout is a transport failure, not a not-deployed answer.
func TestCheckIssuerATimeoutIsNotANotDeployedAnswer(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); slow.Close() })
	err := CheckIssuer(slow.URL, 100*time.Millisecond)
	var nd *IssuerNotDeployedError
	if err == nil || errors.As(err, &nd) {
		t.Fatalf("err = %v", err)
	}
}

// Defence in depth: whatever the caller validated, the check itself never opens a plain-http
// connection to a host that is not loopback, and never contacts an issuer with credentials or a
// path in it.
func TestCheckIssuerRefusesAnIssuerItWouldNotSignInAgainst(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	for _, issuer := range []string{"http://192.0.2.1", "http://id.example.com", srv.URL + "/realm", "ftp://id.example.com", ""} {
		err := CheckIssuer(issuer, time.Second)
		if err == nil {
			t.Errorf("CheckIssuer(%q) accepted", issuer)
			continue
		}
		var nd *IssuerNotDeployedError
		if errors.As(err, &nd) || strings.Contains(err.Error(), "could not reach") {
			t.Errorf("CheckIssuer(%q) = %v; it must refuse before it connects", issuer, err)
		}
	}
	if hits != 0 {
		t.Fatalf("the check contacted a refused issuer %d times", hits)
	}
}

// Pinned: the issuer in the document may differ from the configured one by a trailing slash and by
// the case of the host, nothing else. The document is only checked, never used: every endpoint is
// built from the configured issuer. Strict byte equality would lock everyone out of sign-in over a
// slash, and gains nothing.
func TestCheckIssuerToleratesATrailingSlashAndHostCaseInTheDocumentIssuer(t *testing.T) {
	slash := discoveryServer(t, 200, func(base string) any {
		return map[string]any{"issuer": base + "/", "device_authorization_endpoint": base + "/device/authorize"}
	})
	if err := CheckIssuer(slash.URL, time.Second); err != nil {
		t.Fatalf("a document issuer with a trailing slash was refused: %v", err)
	}
	// Not tolerated: a different origin, scheme or path.
	for name, issuer := range map[string]func(base string) string{
		"a path":         func(b string) string { return b + "/realm" },
		"another scheme": func(b string) string { return "https://" + strings.TrimPrefix(b, "http://") },
		"a prefix match": func(b string) string { return b + ".evil.example" },
	} {
		srv := discoveryServer(t, 200, func(base string) any {
			return map[string]any{"issuer": issuer(base), "device_authorization_endpoint": base + "/device/authorize"}
		})
		if err := CheckIssuer(srv.URL, time.Second); err == nil {
			t.Errorf("%s in the document issuer was accepted", name)
		}
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
