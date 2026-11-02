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
		AuthIssuer: "https://id.example.com",
		AccountID:  "acct-1",
		Roid:       &config.RoidSignIn{Issuer: "https://id.example.com", PersonID: "person-a"},
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
	f, err := credentialFetcher(roidConfig(), "", "cl-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.BaseURL != "https://id.example.com" || f.Account != "acct-1" || f.Cluster != "cl-1" || f.Bearer == nil {
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
