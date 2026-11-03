package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/kubeaccess"
)

func tokenServer(t *testing.T, status int, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func sourceFor(srv *httptest.Server) *kubeaccess.Source {
	return &kubeaccess.Source{Now: time.Now, Fetcher: &kubeaccess.Fetcher{
		BaseURL: srv.URL, Account: "acct-1", Cluster: "cl-1", Bearer: func() (string, error) { return "at_x", nil },
	}}
}

func TestCredentialPrintsOnlyTheExecCredentialOnStdout(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	srv, seen := tokenServer(t, 200, `{"token":"jwt.a.b","expires_at":"`+exp+`"}`)
	var out, errw bytes.Buffer
	if err := emitCredential(&out, &errw, "", sourceFor(srv)); err != nil {
		t.Fatal(err)
	}
	if errw.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errw.String())
	}
	var got struct {
		Kind   string `json:"kind"`
		Status struct {
			Token string `json:"token"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Kind != "ExecCredential" || got.Status.Token != "jwt.a.b" {
		t.Fatalf("stdout = %q (%v)", out.String(), err)
	}
	if len(*seen) != 1 || (*seen)[0] != "POST /v1/accounts/acct-1/clusters/cl-1/k8s/token Bearer at_x" {
		t.Errorf("calls = %v", *seen)
	}
}

func TestCredentialRefusalGoesToStderrAsOnePlainLineWithNoStdout(t *testing.T) {
	srv, _ := tokenServer(t, 403, `{"error":{"code":"authz.denied","reason":"no_grant","message":"x"}}`)
	var out, errw bytes.Buffer
	err := emitCredential(&out, &errw, "", sourceFor(srv))
	if err == nil {
		t.Fatal("want a non-zero exit")
	}
	if out.Len() != 0 {
		t.Errorf("stdout must stay empty on failure, got %q", out.String())
	}
	if lines := strings.Count(strings.TrimRight(errw.String(), "\n"), "\n"); lines != 0 || !strings.Contains(errw.String(), "no Kubernetes access") {
		t.Errorf("stderr = %q", errw.String())
	}
}

func TestCredentialRefusesAnExecInfoItCannotAnswer(t *testing.T) {
	srv, seen := tokenServer(t, 200, `{}`)
	var out, errw bytes.Buffer
	err := emitCredential(&out, &errw, `{"apiVersion":"client.authentication.k8s.io/v1beta1"}`, sourceFor(srv))
	if err == nil || out.Len() != 0 || len(*seen) != 0 {
		t.Fatalf("err = %v out = %q calls = %v", err, out.String(), *seen)
	}
	if !strings.Contains(errw.String(), "runos kube access") {
		t.Errorf("stderr = %q", errw.String())
	}
}

func roidConfig() *config.Config {
	return &config.Config{
		AuthIssuer:   "https://id.example.com",
		ConductorURL: "https://api.example.com",
		AccountID:    "acct-1",
		Roid:         &config.RoidSignIn{Issuer: "https://id.example.com", PersonID: "person-a"},
	}
}

func TestCredentialSetupNeedsASignedInPerson(t *testing.T) {
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	cases := map[string]struct {
		cfg  func() *config.Config
		env  string
		want string
	}{
		"no roid environment": {func() *config.Config { c := roidConfig(); c.AuthIssuer = ""; return c }, "", "does not sign in through the RunOS identity service"},
		"not signed in":       {func() *config.Config { c := roidConfig(); c.Roid = nil; return c }, "", "runos login"},
		"signed in elsewhere": {func() *config.Config { c := roidConfig(); c.Roid.Issuer = "https://other.example.com"; return c }, "", "runos login"},
		"stored api key":      {func() *config.Config { c := roidConfig(); c.Roid = nil; c.APIKey = "k"; return c }, "", "signed-in person"},
		"env api key":         {func() *config.Config { c := roidConfig(); c.Roid = nil; return c }, "key-from-env", "signed-in person"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RUNOS_API_KEY", c.env)
			_, err := credentialFetcher(c.cfg(), "", "cl-1")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCredentialSetupUsesTheRoidSessionEvenWhenAnAPIKeyIsSet(t *testing.T) {
	t.Setenv("RUNOS_API_KEY", "key-from-env")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	t.Setenv("RUNOS_API_URL", "")
	f, err := credentialFetcher(roidConfig(), "", "cl-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.BaseURL != "https://api.example.com" || f.Account != "acct-1" || f.Cluster != "cl-1" || f.Bearer == nil {
		t.Errorf("fetcher = %+v", f)
	}
}

func TestCredentialSetupTakesAccountFromTheFlagFirst(t *testing.T) {
	t.Setenv("RUNOS_API_KEY", "")
	f, err := credentialFetcher(roidConfig(), "acct-2", "cl-1")
	if err != nil || f.Account != "acct-2" {
		t.Fatalf("fetcher = %+v err = %v", f, err)
	}
	if _, err := credentialFetcher(roidConfig(), "", ""); err == nil {
		t.Error("want an error for a missing cluster")
	}
}

func TestCredentialCommandSkipsTheBootstrapAndIsHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNOS_API_KEY", "")
	if !kubeCredentialCmd.Hidden {
		t.Error("the credential command is for kubectl, not for people")
	}
	if kubeCredentialCmd.PersistentPreRunE == nil {
		t.Fatal("without its own PersistentPreRunE the root bootstrap runs: config fetch, manifest fetch, update notice on stderr")
	}
	if err := kubeCredentialCmd.PersistentPreRunE(kubeCredentialCmd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home + "/.runos"); err == nil {
		t.Error("the credential command wrote ~/.runos")
	}
}

func TestKubectlRunsSkipTheManifestLoadAtStartup(t *testing.T) {
	for args, want := range map[string]bool{
		"runos kube credential --account a --cluster c": true,
		"runos kube access":                             false,
		"runos kube":                                    false,
		"runos clusters list":                           false,
		"runos":                                         false,
	} {
		if got := isKubeCredentialRun(strings.Fields(args)); got != want {
			t.Errorf("isKubeCredentialRun(%q) = %v, want %v", args, got, want)
		}
	}
}

// hostSplitServers model prod: the sign-in host answers 404 not_on_this_host to the token route
// (roid serves it only behind rapi), and the API host serves it.
func hostSplitServers(t *testing.T) (issuer, apiHost *httptest.Server, issuerCalls, apiCalls *[]string) {
	t.Helper()
	exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	serve := func(calls *[]string, onRoute func(w http.ResponseWriter)) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*calls = append(*calls, r.Method+" "+r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			onRoute(w)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	issuerCalls, apiCalls = &[]string{}, &[]string{}
	issuer = serve(issuerCalls, func(w http.ResponseWriter) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","reason":"not_on_this_host"}}`))
	})
	apiHost = serve(apiCalls, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"token":"jwt.a.b","expires_at":"` + exp + `"}`))
	})
	return issuer, apiHost, issuerCalls, apiCalls
}

func fetchWith(t *testing.T, cfg *config.Config) (kubeaccess.Issued, error) {
	t.Helper()
	t.Setenv("RUNOS_API_KEY", "")
	t.Setenv("RUNOS_ACCOUNT_ID", "")
	f, err := credentialFetcher(cfg, "", "cl-1")
	if err != nil {
		t.Fatal(err)
	}
	f.Bearer = func() (string, error) { return "at_x", nil } // the session is not under test here
	return f.Fetch()
}

func TestCredentialCallsTheAPIHostNotTheSignInHostForTheTokenRoute(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	issuer, apiHost, issuerCalls, apiCalls := hostSplitServers(t)
	cfg := roidConfig()
	cfg.AuthIssuer, cfg.Roid.Issuer, cfg.ConductorURL = issuer.URL, issuer.URL, apiHost.URL
	iss, err := fetchWith(t, cfg)
	if err != nil || iss.Token != "jwt.a.b" {
		t.Fatalf("token = %+v err = %v", iss, err)
	}
	if len(*issuerCalls) != 0 {
		t.Errorf("the sign-in host got %v; roid serves the token route only behind the API host", *issuerCalls)
	}
	if len(*apiCalls) != 1 || (*apiCalls)[0] != "POST /v1/accounts/acct-1/clusters/cl-1/k8s/token" {
		t.Errorf("API host calls = %v", *apiCalls)
	}
}

// RUNOS_API_URL replaces the stored API URL for the process. The dev environment sends its API host
// to conductor, not to rapi, so a test there points the override at a host that serves the route.
func TestCredentialFollowsTheAPIURLOverride(t *testing.T) {
	issuer, apiHost, issuerCalls, apiCalls := hostSplitServers(t)
	cfg := roidConfig()
	cfg.AuthIssuer, cfg.Roid.Issuer, cfg.ConductorURL = issuer.URL, issuer.URL, "https://api.example.com"
	t.Setenv("RUNOS_API_URL", apiHost.URL)
	if _, err := fetchWith(t, cfg); err != nil {
		t.Fatal(err)
	}
	if len(*issuerCalls) != 0 || len(*apiCalls) != 1 {
		t.Errorf("issuer calls = %v, API calls = %v", *issuerCalls, *apiCalls)
	}
}

// With no API URL at all there is one host, the issuer, as in a deployment without a front service.
func TestCredentialFallsBackToTheIssuerWhenNoAPIURLIsKnown(t *testing.T) {
	t.Setenv("RUNOS_API_URL", "")
	t.Setenv("RUNOS_API_KEY", "")
	cfg := roidConfig()
	cfg.ConductorURL = ""
	f, err := credentialFetcher(cfg, "", "cl-1")
	if err != nil || f.BaseURL != "https://id.example.com" {
		t.Fatalf("fetcher = %+v err = %v", f, err)
	}
}

// The token lives in memory for the run. A token call must leave no file behind in the home
// directory, the working directory or the temp directory.
func TestCredentialRunWritesNothingToDisk(t *testing.T) {
	home, work, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	t.Chdir(work)
	exp := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	srv, _ := tokenServer(t, 200, `{"token":"jwt.a.b","expires_at":"`+exp+`"}`)
	var out, errw bytes.Buffer
	if err := emitCredential(&out, &errw, "", sourceFor(srv)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, work, tmp} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s holds %d entries after a token call", dir, len(entries))
		}
	}
}
