package kubeaccess

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func testCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func testEntry(t *testing.T) Entry {
	return Entry{Name: "runos-cl-1", Server: "https://203.0.113.10:6443", CAPEM: testCA(t), Account: "acct-1", Cluster: "cl-1", Command: "runos"}
}

type kc struct {
	APIVersion     string `yaml:"apiVersion"`
	Kind           string `yaml:"kind"`
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
			CAData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token string `yaml:"token"`
			Exec  struct {
				APIVersion      string   `yaml:"apiVersion"`
				Command         string   `yaml:"command"`
				Args            []string `yaml:"args"`
				InteractiveMode string   `yaml:"interactiveMode"`
				InstallHint     string   `yaml:"installHint"`
			} `yaml:"exec"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

func parse(t *testing.T, b []byte) kc {
	t.Helper()
	var out kc
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatalf("output is not YAML: %v\n%s", err, b)
	}
	return out
}

func TestMergeIntoNothingWritesAFullEntry(t *testing.T) {
	e := testEntry(t)
	out, err := Merge(nil, e, false)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, out)
	if got.APIVersion != "v1" || got.Kind != "Config" {
		t.Errorf("header = %s %s", got.APIVersion, got.Kind)
	}
	if len(got.Clusters) != 1 || got.Clusters[0].Name != "runos-cl-1" || got.Clusters[0].Cluster.Server != "https://203.0.113.10:6443" {
		t.Errorf("clusters = %+v", got.Clusters)
	}
	ca, _ := base64.StdEncoding.DecodeString(got.Clusters[0].Cluster.CAData)
	if string(ca) != string(e.CAPEM) {
		t.Errorf("CA data does not round trip")
	}
	u := got.Users[0].User.Exec
	if got.Users[0].Name != "runos-cl-1" || u.APIVersion != "client.authentication.k8s.io/v1" || u.Command != "runos" || u.InteractiveMode != "Never" {
		t.Errorf("user = %+v", got.Users[0])
	}
	wantArgs := []string{"kube", "credential", "--account", "acct-1", "--cluster", "cl-1"}
	if strings.Join(u.Args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("args = %v", u.Args)
	}
	if u.InstallHint == "" {
		t.Error("want an install hint")
	}
	if got.Contexts[0].Name != "runos-cl-1" || got.Contexts[0].Context.Cluster != "runos-cl-1" || got.Contexts[0].Context.User != "runos-cl-1" {
		t.Errorf("contexts = %+v", got.Contexts)
	}
	if got.CurrentContext != "runos-cl-1" {
		t.Errorf("a new file takes the context as current, got %q", got.CurrentContext)
	}
}

func TestMergeNeverWritesATokenOrASecret(t *testing.T) {
	out, err := Merge(nil, testEntry(t), true)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"token:", "client-key", "client-certificate", "password", "BEGIN"} {
		if strings.Contains(s, bad) {
			t.Errorf("kubeconfig holds %q:\n%s", bad, s)
		}
	}
}

const other = `# my clusters
apiVersion: v1
kind: Config
current-context: prod
preferences: {}
clusters:
- name: other
  cluster:
    server: https://198.51.100.5:6443
    insecure-skip-tls-verify: true
users:
- name: other-admin
  user:
    token: keep-me
contexts:
- name: prod
  context:
    cluster: other
    user: other-admin
    namespace: web
extensions:
- name: keep
  extension: {a: 1}
`

func TestMergeKeepsEveryOtherEntryAndTheCurrentContext(t *testing.T) {
	out, err := Merge([]byte(other), testEntry(t), false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, keep := range []string{"# my clusters", "keep-me", "namespace: web", "insecure-skip-tls-verify: true", "extensions:", "preferences:"} {
		if !strings.Contains(s, keep) {
			t.Errorf("lost %q:\n%s", keep, s)
		}
	}
	got := parse(t, out)
	if got.CurrentContext != "prod" {
		t.Errorf("current-context changed to %q", got.CurrentContext)
	}
	if len(got.Clusters) != 2 || len(got.Users) != 2 || len(got.Contexts) != 2 {
		t.Errorf("counts: %d clusters %d users %d contexts", len(got.Clusters), len(got.Users), len(got.Contexts))
	}
	if got.Clusters[0].Name != "other" || got.Clusters[1].Name != "runos-cl-1" {
		t.Errorf("order = %v", got.Clusters)
	}
}

func TestMergeUseContextSwitchesCurrentContext(t *testing.T) {
	out, err := Merge([]byte(other), testEntry(t), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, out); got.CurrentContext != "runos-cl-1" {
		t.Errorf("current-context = %q", got.CurrentContext)
	}
}

func TestMergeReplacesAnEntryOfTheSameNameOnly(t *testing.T) {
	e := testEntry(t)
	first, err := Merge([]byte(other), e, false)
	if err != nil {
		t.Fatal(err)
	}
	e.Server = "https://203.0.113.99:6443"
	e.Command = "/opt/bin/runos"
	second, err := Merge(first, e, false)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, second)
	if len(got.Clusters) != 2 || len(got.Users) != 2 || len(got.Contexts) != 2 {
		t.Fatalf("a repeat must replace, got %d/%d/%d", len(got.Clusters), len(got.Users), len(got.Contexts))
	}
	if got.Clusters[1].Cluster.Server != "https://203.0.113.99:6443" || got.Users[1].User.Exec.Command != "/opt/bin/runos" {
		t.Errorf("entry not replaced: %+v", got)
	}
	if !strings.Contains(string(second), "keep-me") {
		t.Error("lost an unrelated user")
	}
}

func TestMergeHandlesNullSections(t *testing.T) {
	out, err := Merge([]byte("apiVersion: v1\nkind: Config\nclusters: null\nusers: []\ncontexts:\n"), testEntry(t), false)
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, out)
	if len(got.Clusters) != 1 || len(got.Users) != 1 || len(got.Contexts) != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestMergeRefusesAFileItCannotReadAsAKubeconfig(t *testing.T) {
	for name, in := range map[string]string{
		"list root":       "- a\n- b\n",
		"scalar root":     "hello\n",
		"clusters scalar": "clusters: nope\n",
		"two documents":   "a: 1\n---\nb: 2\n",
		"bad yaml":        "clusters: [\n",
		"entry not map":   "clusters:\n- just-a-string\n",
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := Merge([]byte(in), testEntry(t), false); err == nil {
				t.Fatalf("want an error, got:\n%s", out)
			}
		})
	}
}

func TestEntryValidateRefusesBadInput(t *testing.T) {
	good := testEntry(t)
	cases := map[string]func(*Entry){
		"http server":      func(e *Entry) { e.Server = "http://203.0.113.10:6443" },
		"no host":          func(e *Entry) { e.Server = "https://" },
		"user info":        func(e *Entry) { e.Server = "https://u:p@203.0.113.10:6443" },
		"query":            func(e *Entry) { e.Server = "https://203.0.113.10:6443/?x=1" },
		"not a url":        func(e *Entry) { e.Server = "%%" },
		"ca not pem":       func(e *Entry) { e.CAPEM = []byte("not a cert") },
		"ca empty":         func(e *Entry) { e.CAPEM = nil },
		"ca is a key":      func(e *Entry) { e.CAPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}}) },
		"no name":          func(e *Entry) { e.Name = "" },
		"no account":       func(e *Entry) { e.Account = "" },
		"bad cluster id":   func(e *Entry) { e.Cluster = "Bad_ID" },
		"no command":       func(e *Entry) { e.Command = "" },
		"cluster id slash": func(e *Entry) { e.Cluster = "a/b" },
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("good entry refused: %v", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := good
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestWriteFileMergesAtomicallyAndKeepsPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config")
	if err := WriteFile(path, testEntry(t), false); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	if ds, _ := os.Stat(filepath.Dir(path)); ds.Mode().Perm() != 0o700 {
		t.Errorf("new dir mode = %v, want 0700", ds.Mode().Perm())
	}
	if err := WriteFile(path, testEntry(t), false); err != nil {
		t.Fatalf("second write: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".kubeconfig-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestWriteFileKeepsAnExistingFilesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(other), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, testEntry(t), false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", st.Mode().Perm())
	}
}

func TestWriteFileLeavesAnUnreadableFileAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("- not\n- a kubeconfig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, testEntry(t), false); err == nil {
		t.Fatal("want an error")
	}
	if b, _ := os.ReadFile(path); string(b) != "- not\n- a kubeconfig\n" {
		t.Errorf("file was changed: %q", b)
	}
}

func TestWriteFileFollowsASymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(real, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}
	if err := WriteFile(link, testEntry(t), false); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a file")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "runos-cl-1") {
		t.Error("the target file was not updated")
	}
}

func TestDefaultPathFollowsKubectl(t *testing.T) {
	sep := string(os.PathListSeparator)
	cases := []struct{ kubeconfig, home, want string }{
		{"", "/home/p", filepath.Join("/home/p", ".kube", "config")},
		{"/x/one" + sep + "/x/two", "/home/p", "/x/one"},
		{sep + "/x/two", "/home/p", "/x/two"},
		{"/x/one", "", "/x/one"},
	}
	for _, c := range cases {
		got, err := DefaultPath(func(k string) string {
			if k == "KUBECONFIG" {
				return c.kubeconfig
			}
			return ""
		}, c.home)
		if err != nil || got != c.want {
			t.Errorf("DefaultPath(%q, %q) = %q, %v; want %q", c.kubeconfig, c.home, got, err, c.want)
		}
	}
	if _, err := DefaultPath(func(string) string { return "" }, ""); err == nil {
		t.Error("no home and no KUBECONFIG must be an error")
	}
}
