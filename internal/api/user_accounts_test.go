package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserAccounts(t *testing.T) {
	t.Run("reads every membership with its role and default flag", func(t *testing.T) {
		var gotPath, gotAuth, gotMethod string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
			fmt.Fprint(w, `{"accounts":[
				{"aid":"aaaaa","name":"First","companyName":"Acme","accountRole":"admin","isDefault":true},
				{"aid":"bbbbb","name":"Second","companyName":"","accountRole":"limited","isDefault":false}
			]}`)
		}))
		defer srv.Close()

		accounts, err := NewClient(srv.URL).UserAccounts("id-token")
		if err != nil {
			t.Fatalf("UserAccounts: %v", err)
		}
		if gotMethod != http.MethodGet || gotPath != "/user/accounts" {
			t.Errorf("request = %s %s, want GET /user/accounts", gotMethod, gotPath)
		}
		if gotAuth != "Bearer id-token" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if len(accounts) != 2 {
			t.Fatalf("got %d accounts, want 2", len(accounts))
		}
		if accounts[0].AID != "aaaaa" || !accounts[0].IsDefault || accounts[0].AccountRole != "admin" {
			t.Errorf("first row = %+v", accounts[0])
		}
		if accounts[1].AID != "bbbbb" || accounts[1].IsDefault || accounts[1].AccountRole != "limited" {
			t.Errorf("second row = %+v", accounts[1])
		}
	})

	t.Run("a refusal carries conductor's own words and the status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"Invalid token"}`)
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL).UserAccounts("t")
		if err == nil || !strings.Contains(err.Error(), "Invalid token") || !strings.Contains(err.Error(), "401") {
			t.Fatalf("error = %v, want conductor's message and the status", err)
		}
	})

	t.Run("a 200 without the accounts field is an error, not an empty list", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{}`)
		}))
		defer srv.Close()

		if _, err := NewClient(srv.URL).UserAccounts("t"); err == nil {
			t.Fatal("expected an error for a body with no accounts field")
		}
	})

	t.Run("an empty list is a real answer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"accounts":[]}`)
		}))
		defer srv.Close()

		accounts, err := NewClient(srv.URL).UserAccounts("t")
		if err != nil || len(accounts) != 0 {
			t.Fatalf("accounts = %v, err = %v; want an empty list and no error", accounts, err)
		}
	})
}

func TestUserAccountLabel(t *testing.T) {
	cases := []struct {
		name    string
		account UserAccount
		want    string
	}{
		{"company name wins", UserAccount{AID: "aaaaa", Name: "n", CompanyName: "c"}, "c"},
		{"name when no company", UserAccount{AID: "aaaaa", Name: "n"}, "n"},
		{"empty when neither", UserAccount{AID: "aaaaa"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.account.Label(); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}
