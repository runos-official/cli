package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /:aid/modules is what tells a caller a capability is switched off
// rather than missing, so the enabled flag and a readable failure are the
// two things that matter.
func TestAccountModules(t *testing.T) {
	t.Run("reads the catalogue with its effective enabled state", func(t *testing.T) {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, `{"modules":[
				{"key":"virt","name":"Virtual Machines","tier":"premium","sortOrder":1,"enabled":false},
				{"key":"apps","name":"Applications","tier":"base","sortOrder":0,"enabled":true}
			]}`)
		}))
		defer srv.Close()

		modules, err := NewClient(srv.URL).AccountModules("acct1", "pat-test-token")
		if err != nil {
			t.Fatalf("AccountModules: %v", err)
		}
		if gotPath != "/acct1/modules" {
			t.Errorf("path = %q, want /acct1/modules", gotPath)
		}
		if gotAuth != "Bearer pat-test-token" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if len(modules) != 2 {
			t.Fatalf("got %d modules, want 2", len(modules))
		}
		if modules[0].Key != "virt" || modules[0].Enabled || modules[0].Tier != "premium" {
			t.Errorf("virt row = %+v", modules[0])
		}
		if !modules[1].Enabled {
			t.Errorf("apps row = %+v, want enabled", modules[1])
		}
	})

	t.Run("a refusal carries conductor's own words", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"Admin role required"}`)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL).AccountModules("acct1", "t")
		if err == nil {
			t.Fatal("expected an error on 403")
		}
		if !strings.Contains(err.Error(), "Admin role required") || !strings.Contains(err.Error(), "403") {
			t.Errorf("error = %q, want conductor's message and the status", err)
		}
	})

	t.Run("no account id is refused before any request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("a request was made with no account id")
		}))
		defer srv.Close()

		if _, err := NewClient(srv.URL).AccountModules("", "t"); err == nil {
			t.Fatal("expected an error with no account id")
		}
	})
}
