package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The property that makes the Procedure surface usable: a non-2xx is a
// Result the caller READS, not an error that discards the body. A
// blocked plan is a 409 carrying every failing reason, the
// classification and the plan hash, and a client that dropped that body
// would turn the only explanation a user gets into "request failed".
func TestDoReturnsTheBodyOnANonSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"Blocked by a deterministic safety gate","reasons":["a","b"]}`)
	}))
	defer server.Close()

	result, err := NewClient(server.URL).Do(http.MethodPost, "/x", "token", nil)
	if err != nil {
		t.Fatalf("Do returned an error for a 409; a non-2xx is a Result, not a failure: %v", err)
	}
	if result.OK() {
		t.Fatal("OK() reported a 409 as success")
	}
	var body struct {
		Reasons []string `json:"reasons"`
	}
	if err := result.Decode(&body); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(body.Reasons) != 2 {
		t.Fatalf("reasons = %v, want both", body.Reasons)
	}
}

func TestDoSendsTheBearerTokenAndJSONBody(t *testing.T) {
	var gotAuth, gotContentType, gotBody, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	_, err := NewClient(server.URL).Do(http.MethodPost, "/a/b", "tok-1", map[string]string{"decision": "approve"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q", gotMethod)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("the body was not JSON: %q", gotBody)
	}
	if decoded["decision"] != "approve" {
		t.Fatalf("body = %v", decoded)
	}
}

// A GET must not carry a Content-Type it has no body for.
func TestDoOmitsContentTypeWithoutABody(t *testing.T) {
	var gotContentType string
	var gotLength int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotLength = r.ContentLength
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	if _, err := NewClient(server.URL).Do(http.MethodGet, "/x", "tok", nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotContentType != "" {
		t.Fatalf("Content-Type = %q, want none", gotContentType)
	}
	if gotLength > 0 {
		t.Fatalf("ContentLength = %d, want no body", gotLength)
	}
}

// A base URL with a trailing slash must not produce a doubled slash, and
// the path must be joined verbatim so the caller's escaping survives.
func TestDoJoinsTheBaseURLWithoutDoublingTheSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	if _, err := NewClient(server.URL+"/").Do(http.MethodGet, "/acct/clus/procedures", "tok", nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotPath != "/acct/clus/procedures" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestErrorMessagePrefersTheConductorEnvelope(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"error and reason", `{"error":"Cannot decide","reason":"a PAT cannot approve"}`, "Cannot decide: a PAT cannot approve"},
		{"error only", `{"error":"Not found"}`, "Not found"},
		{"reason only", `{"reason":"the account directory does not show this principal"}`, "the account directory does not show this principal"},
		{"message fallback", `{"message":"upstream failure"}`, "upstream failure"},
		{"no envelope", `{"data":[]}`, ""},
		{"not json", `<html>502</html>`, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := &Result{StatusCode: 409, Body: []byte(testCase.body)}
			if got := result.ErrorMessage(); got != testCase.want {
				t.Fatalf("ErrorMessage = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A proxy's HTML error page must not surface as a bare JSON syntax
// error: the status code is the fact the user needs.
func TestDecodeNamesTheStatusOnANonJSONBody(t *testing.T) {
	result := &Result{StatusCode: 502, Body: []byte("<html>bad gateway</html>")}
	err := result.Decode(&struct{}{})
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("the error does not name the status: %v", err)
	}
}

func TestDecodeRefusesAnEmptyBody(t *testing.T) {
	if err := (&Result{StatusCode: 204}).Decode(&struct{}{}); err == nil {
		t.Fatal("an empty body decoded as if it were a value")
	}
}

func TestOKCoversTheWhole2xxRange(t *testing.T) {
	for status, want := range map[int]bool{199: false, 200: true, 202: true, 204: true, 299: true, 300: false, 409: false} {
		if got := (&Result{StatusCode: status}).OK(); got != want {
			t.Errorf("OK(%d) = %v, want %v", status, got, want)
		}
	}
}

/*
Conductor bounded a Firebase sign-in on 2026-08-25: a session now expires on the same clock the VPN
session uses (auth_time), and says so with a 401 carrying code auth.session_expired plus a header
naming when. A caller branches on the CODE, never on the sentence, which is written for a person
and is expected to be reworded.
*/
func TestSessionExpiredReadsTheCodeNotTheSentence(t *testing.T) {
	expired := &Result{
		StatusCode: http.StatusUnauthorized,
		Body:       []byte(`{"error":"Your session is 30 hours old and sessions expire after 24.","code":"auth.session_expired"}`),
	}
	if !expired.SessionExpired() {
		t.Fatal("a 401 carrying auth.session_expired must read as an expired session")
	}

	// A plain 401 is a DIFFERENT problem with a different fix: a bad or missing credential, not one
	// that simply aged out. Treating them alike would send someone to a browser sign-in to fix a
	// token they never had.
	plain := &Result{StatusCode: http.StatusUnauthorized, Body: []byte(`{"error":"Unauthorized"}`)}
	if plain.SessionExpired() {
		t.Fatal("a plain 401 must not read as an expired session")
	}

	// The same code on a non-401 is not this. And a body that is not JSON must not panic or guess.
	other := &Result{StatusCode: http.StatusForbidden, Body: []byte(`{"code":"auth.session_expired"}`)}
	if other.SessionExpired() {
		t.Fatal("only a 401 carries this meaning")
	}
	if (&Result{StatusCode: http.StatusUnauthorized, Body: []byte("<html>")}).SessionExpired() {
		t.Fatal("a non-JSON body must read as not-expired, not crash")
	}
}

func TestSessionExpiresAtIsAbsentRatherThanGuessed(t *testing.T) {
	header := http.Header{}
	header.Set(SessionExpiresHeader, "2026-08-26T09:59:00Z")
	at := (&Result{StatusCode: 200, Header: header}).SessionExpiresAt()
	if at.UTC().Format(time.RFC3339) != "2026-08-26T09:59:00Z" {
		t.Fatalf("expiry not read back: %v", at)
	}

	// Absence means "no known expiry" (a personal access token, or the rule switched off). It must
	// never come back as a zero time a caller could mistake for "expires now"; callers check
	// IsZero, so the contract is that it stays zero and says nothing.
	if !(&Result{StatusCode: 200, Header: http.Header{}}).SessionExpiresAt().IsZero() {
		t.Fatal("a missing header must leave the expiry unknown")
	}
	if !(&Result{StatusCode: 200}).SessionExpiresAt().IsZero() {
		t.Fatal("no headers at all must leave the expiry unknown")
	}
	bad := http.Header{}
	bad.Set(SessionExpiresHeader, "not a time")
	if !(&Result{StatusCode: 200, Header: bad}).SessionExpiresAt().IsZero() {
		t.Fatal("an unparseable header must leave the expiry unknown")
	}
}
