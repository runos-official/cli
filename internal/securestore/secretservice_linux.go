//go:build linux

package securestore

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SecretService stores items in the freedesktop Secret Service (GNOME
// Keyring, KWallet) through secret-tool(1). The secret goes in on stdin.
// Built for Linux; not yet run against a Secret Service.
type SecretService struct{ Service string }

// NewSecretService returns a Secret Service store for one service name.
func NewSecretService(service string) *SecretService { return &SecretService{Service: service} }

// Kind names the backend.
func (s *SecretService) Kind() string { return "linux-secret-service" }

// Available needs secret-tool and a session bus. A headless container has
// neither, so Open then refuses with cli.no_secure_store.
func (s *SecretService) Available() bool {
	if _, err := exec.LookPath("secret-tool"); err != nil {
		return false
	}
	return os.Getenv("DBUS_SESSION_BUS_ADDRESS") != ""
}

func (s *SecretService) attrs(item string) ([]string, error) {
	if err := checkName("service", s.Service); err != nil {
		return nil, err
	}
	if err := checkName("item", item); err != nil {
		return nil, err
	}
	return []string{"service", s.Service, "account", item}, nil
}

func (s *SecretService) Set(item string, secret []byte) error {
	a, err := s.attrs(item)
	if err != nil {
		return err
	}
	cmd := exec.Command("secret-tool", append([]string{"store", "--label=" + s.Service}, a...)...)
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(secret))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("securestore: secret-tool store: %v: %s", err, stderr.String())
	}
	got, err := s.Get(item)
	if err != nil || !bytes.Equal(got, secret) {
		return errors.New("securestore: secret service read-back failed")
	}
	return nil
}

func (s *SecretService) Get(item string) ([]byte, error) {
	a, err := s.attrs(item)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("secret-tool", append([]string{"lookup"}, a...)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 && len(out) == 0 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("securestore: secret-tool lookup: %v", err)
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

func (s *SecretService) Delete(item string) error {
	if _, err := s.Get(item); err != nil {
		return err
	}
	a, _ := s.attrs(item)
	return exec.Command("secret-tool", append([]string{"clear"}, a...)...).Run()
}

func platformStore(service string) (Store, bool) {
	s := NewSecretService(service)
	return s, s.Available()
}
