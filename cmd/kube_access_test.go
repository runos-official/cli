package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/kubeaccess"
)

func writeCA(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(p, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseAccess(t *testing.T) kubeAccessOptions {
	return kubeAccessOptions{
		Account: "acct-1", Cluster: "cl-1", Server: "https://203.0.113.10:6443", CAFile: writeCA(t),
		Kubeconfig: filepath.Join(t.TempDir(), "kube", "config"), Command: "runos",
	}
}

func TestKubeAccessWritesTheEntryAndSaysHowToUseIt(t *testing.T) {
	o := baseAccess(t)
	var out bytes.Buffer
	if err := runKubeAccess(&out, o); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.Kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: runos-cl-1", "server: https://203.0.113.10:6443", "kube", "credential", "acct-1"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("kubeconfig lacks %q", want)
		}
	}
	if strings.Contains(string(b), "token:") {
		t.Error("a token was written")
	}
	for _, want := range []string{o.Kubeconfig, "kubectl --context runos-cl-1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestKubeAccessNameFlagAndUseContext(t *testing.T) {
	o := baseAccess(t)
	o.Name, o.UseContext = "my-ctx", true
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(o.Kubeconfig)
	if !strings.Contains(string(b), "current-context: my-ctx") || !strings.Contains(string(b), "name: my-ctx") {
		t.Errorf("kubeconfig:\n%s", b)
	}
}

func TestKubeAccessRefusesBadInputWithoutWriting(t *testing.T) {
	cases := map[string]func(*kubeAccessOptions){
		"no server":   func(o *kubeAccessOptions) { o.Server = "" },
		"http server": func(o *kubeAccessOptions) { o.Server = "http://203.0.113.10:6443" },
		"no ca file":  func(o *kubeAccessOptions) { o.CAFile = "" },
		"missing ca":  func(o *kubeAccessOptions) { o.CAFile = filepath.Join(t.TempDir(), "nope") },
		"ca not a cert": func(o *kubeAccessOptions) {
			p := filepath.Join(t.TempDir(), "x")
			_ = os.WriteFile(p, []byte("x"), 0o600)
			o.CAFile = p
		},
		"no cluster": func(o *kubeAccessOptions) { o.Cluster = "" },
		"no account": func(o *kubeAccessOptions) { o.Account = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := baseAccess(t)
			mutate(&o)
			if err := runKubeAccess(&bytes.Buffer{}, o); err == nil {
				t.Fatal("want an error")
			}
			if _, err := os.Stat(o.Kubeconfig); err == nil {
				t.Error("a kubeconfig was written")
			}
		})
	}
}

func TestKubeAccessIsAHandWrittenCommandBesideClustersKubeconfig(t *testing.T) {
	if kubeAccessCmd.Hidden {
		t.Error("kube access is the command people run")
	}
	if kubeAccessCmd.Name() != "access" || kubeAccessCmd.Parent() != kubeCmd {
		t.Error("kube access must hang under kube, not clusters")
	}
	if f := kubeAccessCmd.Flags().Lookup("ca-file"); f == nil {
		t.Error("missing --ca-file")
	}
	if f := kubeAccessCmd.Flags().Lookup("server"); f == nil {
		t.Error("missing --server")
	}
}

// fakeKubeConnection answers GET /{aid}/{cid}/kube-connection and counts the calls.
func fakeKubeConnection(t *testing.T, status int, body string) (*kubeaccess.ConnectionFetcher, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &kubeaccess.ConnectionFetcher{BaseURL: srv.URL, Account: "acct-1", Cluster: "cl-1", Token: func() (string, error) { return "t", nil }}, &calls
}

func routeBody(t *testing.T, extra string) string {
	b, _ := json.Marshal(string(testCAPEM(t)))
	return `{"server":"https://k8s.cl-1.example.com:6443","ca_pem":` + string(b) + extra + `}`
}

func TestKubeAccessWithNoFlagsTakesServerAndCAFromTheConductor(t *testing.T) {
	o := baseAccess(t)
	o.Server, o.CAFile = "", ""
	var calls *int
	o.Connection, calls = fakeKubeConnection(t, 200, routeBody(t, ""))
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(o.Kubeconfig)
	if *calls != 1 || !strings.Contains(string(b), "server: https://k8s.cl-1.example.com:6443") || !strings.Contains(string(b), "certificate-authority-data:") {
		t.Errorf("calls = %d, kubeconfig:\n%s", *calls, b)
	}
	if strings.Contains(string(b), "tls-server-name") {
		t.Error("the route sent no tls_server_name, so the entry must have none")
	}
}

func TestKubeAccessSetsTLSServerNameFromTheRoute(t *testing.T) {
	o := baseAccess(t)
	o.Server, o.CAFile = "", ""
	o.Connection, _ = fakeKubeConnection(t, 200, strings.Replace(routeBody(t, `,"tls_server_name":"kubernetes"`), "https://k8s.cl-1.example.com:6443", "https://192.0.2.10:6443", 1))
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(o.Kubeconfig)
	if !strings.Contains(string(b), "tls-server-name: kubernetes") || !strings.Contains(string(b), "https://192.0.2.10:6443") {
		t.Errorf("kubeconfig:\n%s", b)
	}
}

func TestKubeAccessFlagsOverrideTheRoute(t *testing.T) {
	// Both flags: the conductor is not asked at all.
	o := baseAccess(t)
	var calls *int
	o.Connection, calls = fakeKubeConnection(t, 200, routeBody(t, `,"tls_server_name":"kubernetes"`))
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(o.Kubeconfig)
	if *calls != 0 || !strings.Contains(string(b), "https://203.0.113.10:6443") || strings.Contains(string(b), "tls-server-name") {
		t.Errorf("calls = %d, kubeconfig:\n%s", *calls, b)
	}

	// One flag: the route fills the other. A --server override drops the route's
	// tls_server_name, which belongs to the route's address, not to the override.
	o = baseAccess(t)
	o.CAFile = ""
	o.Connection, calls = fakeKubeConnection(t, 200, routeBody(t, `,"tls_server_name":"kubernetes"`))
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(o.Kubeconfig)
	if *calls != 1 || !strings.Contains(string(b), "https://203.0.113.10:6443") || strings.Contains(string(b), "tls-server-name") || !strings.Contains(string(b), "certificate-authority-data:") {
		t.Errorf("--server only: calls = %d, kubeconfig:\n%s", *calls, b)
	}

	// An explicit --tls-server-name wins over the route.
	o = baseAccess(t)
	o.TLSServerName = "custom"
	if err := runKubeAccess(&bytes.Buffer{}, o); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(o.Kubeconfig); !strings.Contains(string(b), "tls-server-name: custom") {
		t.Errorf("kubeconfig:\n%s", b)
	}
}

func TestKubeAccessMapsConductorRefusalsToPlainMessagesAndWritesNothing(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"not configured": {409, `{"code":"cluster.api_endpoint_not_configured","error":"x"}`, "--ca-file"},
		"forbidden":      {403, `{"error":"Forbidden"}`, "may not read"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			o := baseAccess(t)
			o.Server, o.CAFile = "", ""
			o.Connection, _ = fakeKubeConnection(t, c.status, c.body)
			err := runKubeAccess(&bytes.Buffer{}, o)
			if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "{") {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if _, serr := os.Stat(o.Kubeconfig); serr == nil {
				t.Error("a kubeconfig was written")
			}
		})
	}
}
