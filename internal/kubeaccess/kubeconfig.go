package kubeaccess

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry is what 'runos kube access' adds to a kubeconfig: one cluster, one
// user and one context, all named Name. The user runs Command, which asks
// for a token each time kubectl needs one, so the file holds no secret.
type Entry struct {
	Name   string
	Server string // https URL of the API server
	CAPEM  []byte // PEM certificate(s) that sign the API server's certificate
	// TLSServerName is the name to check the API server certificate against.
	// It is set only when Server is a node address; empty otherwise.
	TLSServerName string
	Account       string
	Cluster       string // RunOS cluster id
	Command       string // the CLI as kubectl finds it, "runos" by default
}

// clusterID is the shape roid accepts for a cluster id in its token route.
var clusterID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Validate refuses an entry that would write a broken or unsafe kubeconfig.
func (e Entry) Validate() error {
	if e.Name == "" {
		return errors.New("the kubeconfig entry needs a name")
	}
	if e.Account == "" {
		return errors.New("no account selected: pass --account or run 'runos account switch'")
	}
	if !clusterID.MatchString(e.Cluster) {
		return errors.New("the cluster id is not valid: pass --cluster with a RunOS cluster id")
	}
	if e.Command == "" {
		return errors.New("the kubeconfig entry needs the command kubectl runs")
	}
	u, err := url.Parse(e.Server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the API server must be an https URL such as https://203.0.113.10:6443, with no user, query or fragment")
	}
	return validateCA(e.CAPEM)
}

func validateCA(p []byte) error {
	found := false
	for {
		var block *pem.Block
		block, p = pem.Decode(p)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("the CA file holds a %s block: it must hold certificates only", strings.ToLower(block.Type))
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("the CA file holds a certificate that does not parse: %w", err)
		}
		found = true
	}
	if !found {
		return errors.New("the CA file holds no PEM certificate")
	}
	return nil
}

// DefaultPath is where kubectl reads its kubeconfig: the first path in
// KUBECONFIG, else ~/.kube/config.
func DefaultPath(env func(string) string, home string) (string, error) {
	for _, p := range filepath.SplitList(env("KUBECONFIG")) {
		if p != "" {
			return p, nil
		}
	}
	if home == "" {
		return "", errors.New("cannot find a home directory: pass --kubeconfig")
	}
	return filepath.Join(home, ".kube", "config"), nil
}

// Merge adds e to a kubeconfig document and returns the new document. An
// entry of the same name is replaced; nothing else is touched. The current
// context changes only when use is set or the file names none.
func Merge(existing []byte, e Entry, use bool) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	root, err := parseRoot(existing)
	if err != nil {
		return nil, err
	}
	if err := upsert(root, "clusters", e.Name, "cluster", clusterBody(e)); err != nil {
		return nil, err
	}
	if err := upsert(root, "users", e.Name, "user", userBody(e)); err != nil {
		return nil, err
	}
	ctx := map[string]string{"cluster": e.Name, "user": e.Name}
	if err := upsert(root, "contexts", e.Name, "context", ctx); err != nil {
		return nil, err
	}
	if cur := valueOf(root, "current-context"); use || cur == "" {
		setScalar(root, "current-context", e.Name)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

type clusterEntry struct {
	Server        string `yaml:"server"`
	CAData        string `yaml:"certificate-authority-data"`
	TLSServerName string `yaml:"tls-server-name,omitempty"`
}

func clusterBody(e Entry) clusterEntry {
	return clusterEntry{Server: strings.TrimRight(e.Server, "/"), CAData: base64.StdEncoding.EncodeToString(e.CAPEM), TLSServerName: e.TLSServerName}
}

type execEntry struct {
	APIVersion      string   `yaml:"apiVersion"`
	Command         string   `yaml:"command"`
	Args            []string `yaml:"args"`
	InteractiveMode string   `yaml:"interactiveMode"`
	InstallHint     string   `yaml:"installHint"`
}

func userBody(e Entry) map[string]execEntry {
	return map[string]execEntry{"exec": {
		APIVersion:      ExecAPIVersion,
		Command:         e.Command,
		Args:            []string{"kube", "credential", "--account", e.Account, "--cluster", e.Cluster},
		InteractiveMode: "Never",
		InstallHint:     "Install the RunOS CLI and sign in with 'runos login'. kubectl asks it for a short token each time.",
	}}
}

// parseRoot reads one kubeconfig document. Nothing, or only whitespace and
// comments, becomes a fresh document.
func parseRoot(b []byte) (*yaml.Node, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return freshRoot(), nil
		}
		return nil, fmt.Errorf("the kubeconfig is not valid YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("the kubeconfig holds more than one YAML document, so it was left alone")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the file is not a kubeconfig (its top level is not a mapping), so it was left alone")
	}
	return doc.Content[0], nil
}

func freshRoot() *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(root, "apiVersion", "v1")
	setScalar(root, "kind", "Config")
	return root
}

func field(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func valueOf(m *yaml.Node, key string) string {
	if _, v := field(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func setScalar(m *yaml.Node, key, val string) {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val}
	if _, v := field(m, key); v != nil {
		*v = *node
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
}

// upsert puts {name: name, <bodyKey>: body} into the list under key, in
// place of an item of that name or at the end.
func upsert(root *yaml.Node, key, name, bodyKey string, body any) error {
	item := &yaml.Node{}
	if err := item.Encode(map[string]any{"name": name}); err != nil {
		return err
	}
	var bn yaml.Node
	if err := bn.Encode(body); err != nil {
		return err
	}
	item.Content = append(item.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: bodyKey}, &bn)

	_, list := field(root, key)
	switch {
	case list == nil:
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{item}})
		return nil
	case list.Kind == yaml.ScalarNode && list.Tag == "!!null":
		*list = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{item}}
		return nil
	case list.Kind != yaml.SequenceNode:
		return fmt.Errorf("the kubeconfig's %s is not a list, so it was left alone", key)
	}
	for i, it := range list.Content {
		if it.Kind != yaml.MappingNode {
			return fmt.Errorf("an item of the kubeconfig's %s is not a mapping, so it was left alone", key)
		}
		if valueOf(it, "name") == name {
			list.Content[i] = item
			return nil
		}
	}
	list.Content = append(list.Content, item)
	return nil
}

// WriteFile merges e into the kubeconfig at path. The write is atomic: a
// temporary file in the same directory replaces the old one, so a crash or
// a second writer never leaves half a file. A new file is 0600, an old one
// keeps its mode, and a symlink keeps pointing where it did.
func WriteFile(path string, e Entry, use bool) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	existing, err := os.ReadFile(path)
	mode := os.FileMode(0o600)
	switch {
	case err == nil:
		if st, serr := os.Stat(path); serr == nil {
			mode = st.Mode().Perm()
		}
	case os.IsNotExist(err):
		existing = nil
	default:
		return err
	}
	out, err := Merge(existing, e, use)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".kubeconfig-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
