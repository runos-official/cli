package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSendCarriesAFormBodyAndExtraHeaders(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	res, err := NewClient(srv.URL).Send(Request{Method: http.MethodPost, Path: "/device/authorize",
		Form: url.Values{"client_id": {"runos-cli"}}, Header: http.Header{"X-Test": {"one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", res.StatusCode)
	}
	if got.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || body != "client_id=runos-cli" {
		t.Fatalf("content type %q, body %q", got.Header.Get("Content-Type"), body)
	}
	if got.Header.Get("X-Test") != "one" {
		t.Fatalf("extra header lost: %v", got.Header)
	}
	// No token means no Authorization header at all, not "Bearer ".
	if v, ok := got.Header["Authorization"]; ok {
		t.Fatalf("Authorization sent without a token: %q", v)
	}
}

func TestSendRefusesAFormAndAJSONBodyTogether(t *testing.T) {
	_, err := NewClient("http://127.0.0.1:1").Send(Request{Method: http.MethodPost, Path: "/x", JSON: map[string]string{}, Form: url.Values{}})
	if err == nil {
		t.Fatal("a request with two bodies was accepted")
	}
}

func TestDoStillSendsTheBearerAndJSON(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r }))
	defer srv.Close()
	if _, err := NewClient(srv.URL).Do(http.MethodPost, "/x", "tok", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Authorization") != "Bearer tok" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", got.Header)
	}
}
