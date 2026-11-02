package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
