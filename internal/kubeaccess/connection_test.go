package kubeaccess

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func conductor(t *testing.T, status int, body string) (*ConnectionFetcher, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &ConnectionFetcher{BaseURL: srv.URL, Account: "acct-1", Cluster: "cl-1", Token: func() (string, error) { return "tok", nil }}, &seen
}

func TestConnectionReadsServerAndCAFromTheConductorRoute(t *testing.T) {
	f, seen := conductor(t, 200, `{"server":"https://k8s.cl-1.example.com:6443","ca_pem":"-----BEGIN CERTIFICATE-----\nAA\n-----END CERTIFICATE-----\n"}`)
	got, err := f.Fetch()
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || (*seen)[0] != "GET /acct-1/cl-1/kube-connection Bearer tok" {
		t.Errorf("calls = %v", *seen)
	}
	if got.Server != "https://k8s.cl-1.example.com:6443" || !strings.Contains(string(got.CAPEM), "BEGIN CERTIFICATE") || got.TLSServerName != "" {
		t.Errorf("connection = %+v", got)
	}
}

func TestConnectionCarriesTLSServerNameWhenTheRouteSendsOne(t *testing.T) {
	f, _ := conductor(t, 200, `{"server":"https://192.0.2.10:6443","ca_pem":"pem","tls_server_name":"kubernetes"}`)
	got, err := f.Fetch()
	if err != nil || got.TLSServerName != "kubernetes" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestConnectionRefusesAnAnswerWithoutServerOrCA(t *testing.T) {
	for _, body := range []string{`{"server":"https://x"}`, `{"ca_pem":"p"}`, `nope`, ``} {
		f, _ := conductor(t, 200, body)
		if _, err := f.Fetch(); err == nil {
			t.Errorf("body %q: want an error", body)
		}
	}
}

func TestConnectionEscapesIDsAndPassesSessionErrorsThrough(t *testing.T) {
	f, seen := conductor(t, 200, `{"server":"s","ca_pem":"p"}`)
	f.Account, f.Cluster = "a/b", "c d"
	if _, err := f.Fetch(); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0] != "GET /a%2Fb/c%20d/kube-connection Bearer tok" {
		t.Errorf("call = %v", *seen)
	}
	boom := errors.New("no credential")
	f.Token = func() (string, error) { return "", boom }
	if _, err := f.Fetch(); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestConnectionMessageMapsEachConductorAnswerToAPlainLine(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"not configured", 409, `{"code":"cluster.api_endpoint_not_configured","error":"internal words"}`, []string{"API server", "--server", "--ca-file"}},
		{"forbidden", 403, `{"error":"Forbidden"}`, []string{"may not read", "--server"}},
		{"unauthorised", 401, `{"error":"x"}`, []string{"runos login"}},
		{"unknown cluster", 404, `{"error":"x"}`, []string{"does not hold", "--cluster"}},
		{"server error", 500, `<html>`, []string{"500", "--server"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := conductor(t, c.status, c.body)
			_, err := f.Fetch()
			if err == nil {
				t.Fatal("want an error")
			}
			got := ConnectionMessage(err)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("message %q lacks %q", got, w)
				}
			}
			if strings.ContainsAny(got, "{}<>") || strings.Contains(got, "internal words") {
				t.Errorf("message leaked a body: %q", got)
			}
		})
	}
	if got := ConnectionMessage(&TransportError{Err: errors.New("dial tcp: refused")}); !strings.Contains(got, "Could not reach RunOS") {
		t.Errorf("transport message = %q", got)
	}
	if got := ConnectionMessage(errors.New("store locked")); !strings.Contains(got, "store locked") {
		t.Errorf("other message = %q", got)
	}
}
