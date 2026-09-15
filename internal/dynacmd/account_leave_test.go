package dynacmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/auth"
	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/manifest"
)

/*
`runos account leave` comes from the manifest. Conductor ends the login's membership, and the CLI
used to stop there: the left account stayed active with its stored sign-in, `account list` still
showed it, and the next command was refused. These tests drive the real Execute against a stubbed
conductor and a stubbed token refresh. Every id and token below is a placeholder.
*/

const leaveSession = "SESSION"

var accountLeaveCmd = manifest.Command{
	Command:  "account/leave",
	Endpoint: "/:aid/account/membership",
	Method:   http.MethodDelete,
	Input:    &manifest.Input{Fields: []manifest.Field{}},
	Output: &manifest.Output{Type: "object", Fields: []manifest.OutputField{
		{Name: "left"}, {Name: "defaultAccount"}, {Name: "remainingAccounts"},
	}},
}

// leaveStub answers the token refresh and DELETE /<aid>/account/membership with body, and 404s
// anything else.
func leaveStub(t *testing.T, aid, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			fmt.Fprintf(w, `{"id_token":"ID","refresh_token":%q,"expires_in":"3600"}`, leaveSession)
			return
		}
		if r.Method != http.MethodDelete || r.URL.Path != "/"+aid+"/account/membership" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"no such route"}`)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	auth.SetEndpointsForTest(t, srv.URL+"/signin", srv.URL+"/token")
	return localhostURL(srv.URL)
}

// leaveEnv saves the config build produces under a throwaway home, and clears every environment
// variable the config getters prefer.
func leaveEnv(t *testing.T, apiURL string, build func(cfg *config.Config)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_CLUSTER_ID", "")
	t.Setenv("RUNOS_API_URL", "")
	cfg := &config.Config{ConductorURL: apiURL}
	build(cfg)
	if err := cfg.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// signInOn makes a session active on one account and stores it for every member account, the way a
// sign-in does.
func signInOn(cfg *config.Config, active string, members ...string) {
	firebase := &config.FirebaseConfig{APIKey: "KEY", ProjectID: "proj"}
	cfg.ApplySessionLogin(active, firebase, leaveSession, "2026-01-03T00:00:00Z")
	cfg.ShareSessionWithAccounts(members, firebase, leaveSession, "2026-01-03T00:00:00Z")
}

func runLeave(t *testing.T, apiURL string) (stdout, stderr string) {
	t.Helper()
	cmd := warnCmd(t, accountLeaveCmd, false, false)
	read := captureSplit(t)
	err := NewExecutor(apiURL).Execute(cmd, nil, accountLeaveCmd)
	stdout, stderr = read()
	if err != nil {
		t.Fatalf("Execute: %v (stderr %q)", err, stderr)
	}
	return stdout, stderr
}

func loadLeaveConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func activeKnownAccounts(cfg *config.Config) []string {
	var active []string
	for _, account := range cfg.KnownAccounts {
		if account.Active {
			active = append(active, account.AccountID)
		}
	}
	return active
}

func TestLeavingTheActiveAccountMovesToTheDefaultAccount(t *testing.T) {
	apiURL := leaveStub(t, "aaaaa", `{"left":"aaaaa","defaultAccount":"bbbbb","remainingAccounts":1}`)
	leaveEnv(t, apiURL, func(cfg *config.Config) {
		signInOn(cfg, "aaaaa", "aaaaa", "bbbbb")
		cfg.DefaultClusterID = "cluster1"
	})

	stdout, stderr := runLeave(t, apiURL)

	cfg := loadLeaveConfig(t)
	if cfg.AccountID != "bbbbb" || cfg.RefreshToken != leaveSession || cfg.Firebase == nil {
		t.Fatalf("active = %q with token %q and firebase %+v, want bbbbb on the same session", cfg.AccountID, cfg.RefreshToken, cfg.Firebase)
	}
	if cfg.HasStoredCredential("aaaaa") {
		t.Error("the credential for the account the login left is still stored")
	}
	if !cfg.HasStoredCredential("bbbbb") {
		t.Error("the default account has no stored credential")
	}
	if got := activeKnownAccounts(cfg); len(got) != 1 || got[0] != "bbbbb" {
		t.Errorf("active known accounts = %v, want [bbbbb]", got)
	}
	// Cluster ids are account-scoped, so the left account's default cluster is guaranteed wrong.
	if cfg.DefaultClusterID != "" {
		t.Errorf("default cluster %q survived the account change", cfg.DefaultClusterID)
	}
	if !strings.Contains(stderr, "bbbbb") {
		t.Errorf("stderr does not name the new active account: %q", stderr)
	}
	if !strings.Contains(stdout, "left") {
		t.Errorf("the response body was not rendered: %q", stdout)
	}
}

func TestLeavingTheLastAccountSignsOut(t *testing.T) {
	apiURL := leaveStub(t, "aaaaa", `{"left":"aaaaa","defaultAccount":null,"remainingAccounts":0}`)
	leaveEnv(t, apiURL, func(cfg *config.Config) {
		cfg.ApplySessionLogin("ppppp", &config.FirebaseConfig{APIKey: "KEY"}, "OTHER-LOGIN", "2026-01-01T00:00:00Z")
		cfg.ApplyAPIKeyLogin("kkkkk", "pat-k", "2026-01-02T00:00:00Z")
		signInOn(cfg, "aaaaa", "aaaaa")
		cfg.DefaultClusterID = "cluster1"
	})

	_, stderr := runLeave(t, apiURL)

	cfg := loadLeaveConfig(t)
	if cfg.AccountID != "" || cfg.RefreshToken != "" || cfg.Firebase != nil || cfg.DefaultClusterID != "" {
		t.Fatalf("still signed in: account %q, token %q, firebase %+v, cluster %q", cfg.AccountID, cfg.RefreshToken, cfg.Firebase, cfg.DefaultClusterID)
	}
	if cfg.HasStoredCredential("aaaaa") {
		t.Error("the credential for the account the login left is still stored")
	}
	if got := activeKnownAccounts(cfg); len(got) != 0 {
		t.Errorf("active known accounts = %v, want none", got)
	}
	// Conductor revoked THIS login's refresh tokens. Another login and an API key still work.
	if !cfg.HasStoredCredential("ppppp") || !cfg.HasStoredCredential("kkkkk") {
		t.Error("signing out of the leaving login threw away another login's sign-in or an API key")
	}
	if !strings.Contains(stderr, "runos login") {
		t.Errorf("stderr does not say the CLI signed out and how to sign in: %q", stderr)
	}
}

func TestLeavingAnAccountThatIsNotActiveKeepsTheActiveOne(t *testing.T) {
	apiURL := leaveStub(t, "bbbbb", `{"left":"bbbbb","defaultAccount":"aaaaa","remainingAccounts":1}`)
	leaveEnv(t, apiURL, func(cfg *config.Config) {
		signInOn(cfg, "aaaaa", "aaaaa", "bbbbb")
		cfg.DefaultClusterID = "cluster1"
	})
	// RUNOS_ACCOUNT_ID addresses a request to an account the config is not on.
	t.Setenv("RUNOS_ACCOUNT_ID", "bbbbb")

	_, stderr := runLeave(t, apiURL)

	cfg := loadLeaveConfig(t)
	if cfg.AccountID != "aaaaa" || cfg.RefreshToken != leaveSession || cfg.DefaultClusterID != "cluster1" {
		t.Fatalf("the active account changed: %q token %q cluster %q", cfg.AccountID, cfg.RefreshToken, cfg.DefaultClusterID)
	}
	if cfg.HasStoredCredential("bbbbb") {
		t.Error("the credential for the account the login left is still stored")
	}
	if !cfg.HasStoredCredential("aaaaa") {
		t.Error("leaving another account threw away the active account's credential")
	}
	if strings.Contains(stderr, "runos login") {
		t.Errorf("leaving a non-active account reported a sign-out: %q", stderr)
	}
}
