package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The node read only decorates a confirmation prompt, so the caller needs
// two answers from it: the name when the read works, and an error on every
// other outcome. A silent empty string on a failure would let the prompt
// claim the node has no name.
func TestNodeName(t *testing.T) {
	t.Run("reads the name from the node record", func(t *testing.T) {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, `{"nid":"node-1","name":"node-alpha","hostname":"host-alpha"}`)
		}))
		defer srv.Close()

		name, err := NewClient(srv.URL).NodeName("acct1", "cluster1", "node-1", "pat-test-token")
		if err != nil {
			t.Fatalf("NodeName: %v", err)
		}
		if gotPath != "/acct1/cluster1/nodes/node-1" {
			t.Errorf("path = %q, want /acct1/cluster1/nodes/node-1", gotPath)
		}
		if gotAuth != "Bearer pat-test-token" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if name != "node-alpha" {
			t.Errorf("name = %q, want node-alpha", name)
		}
	})

	t.Run("a node with no assigned name reads as the empty string", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"nid":"node-1","name":"","hostname":"host-alpha"}`)
		}))
		defer srv.Close()

		name, err := NewClient(srv.URL).NodeName("acct1", "cluster1", "node-1", "t")
		if err != nil {
			t.Fatalf("NodeName: %v", err)
		}
		if name != "" {
			t.Errorf("name = %q, want the empty string", name)
		}
	})

	// An unknown node id answers 404 and a node id that fails the shape
	// guard answers 400. The caller branches on "not 2xx", so both read
	// the same way here.
	t.Run("a result that is not a success is an error", func(t *testing.T) {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusForbidden} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"Node not found"}`)
			}))
			_, err := NewClient(srv.URL).NodeName("acct1", "cluster1", "node-1", "t")
			srv.Close()
			if err == nil {
				t.Fatalf("expected an error on HTTP %d", status)
			}
			if !strings.Contains(err.Error(), "Node not found") || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Errorf("HTTP %d error = %q, want conductor's message and the status", status, err)
			}
		}
	})

	t.Run("a body that is not the node record is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `<html>proxy error page</html>`)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL).NodeName("acct1", "cluster1", "node-1", "t")
		if err == nil {
			t.Fatal("expected an error on an unparseable body")
		}
	})

	t.Run("a missing id makes no request", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests++
			fmt.Fprint(w, `{"name":"node-alpha"}`)
		}))
		defer srv.Close()

		cases := []struct{ account, cluster, node string }{
			{"", "cluster1", "node-1"},
			{"acct1", "", "node-1"},
			{"acct1", "cluster1", ""},
		}
		for _, tc := range cases {
			if _, err := NewClient(srv.URL).NodeName(tc.account, tc.cluster, tc.node, "t"); err == nil {
				t.Errorf("NodeName(%q, %q, %q) = nil error, want an error", tc.account, tc.cluster, tc.node)
			}
		}
		if requests != 0 {
			t.Errorf("made %d requests, want 0", requests)
		}
	})
}
