//go:build darwin

package securestore

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Keychain stores items as macOS generic passwords through security(1),
// in the user's default keychain. The secret goes in on stdin, through the
// tool's interactive mode, so it never appears in argv or ps output. The
// value is base64 so it needs no quoting.
type Keychain struct{ Service string }

// NewKeychain returns a keychain store for one service name.
func NewKeychain(service string) *Keychain { return &Keychain{Service: service} }

// Kind names the backend.
func (k *Keychain) Kind() string { return "macos-keychain" }

// Available reports whether security(1) works and a default keychain
// exists. Over SSH without a GUI session the keychain may be locked; Set
// then fails and the read-back catches it.
func (k *Keychain) Available() bool {
	if _, err := exec.LookPath("security"); err != nil {
		return false
	}
	out, err := exec.Command("security", "default-keychain").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// addCommand returns the argv and the stdin script that store secret.
func (k *Keychain) addCommand(item string, secret []byte) ([]string, string, error) {
	if err := checkName("service", k.Service); err != nil {
		return nil, "", err
	}
	if err := checkName("item", item); err != nil {
		return nil, "", err
	}
	enc := base64.StdEncoding.EncodeToString(secret)
	script := fmt.Sprintf("add-generic-password -U -s \"%s\" -a \"%s\" -l \"%s\" -w \"%s\"\n", k.Service, item, k.Service, enc)
	return []string{"security", "-i"}, script, nil
}

// Set stores or replaces an item, then reads it back: the exit status of
// the interactive mode is not trusted alone.
func (k *Keychain) Set(item string, secret []byte) error {
	argv, script, err := k.addCommand(item, secret)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("securestore: keychain add failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	got, err := k.Get(item)
	if err != nil {
		return fmt.Errorf("securestore: keychain read-back failed: %w", err)
	}
	if !bytes.Equal(got, secret) {
		return errors.New("securestore: keychain read-back differs")
	}
	return nil
}

// Get reads an item. The secret comes back on stdout.
func (k *Keychain) Get(item string) ([]byte, error) {
	if err := checkName("item", item); err != nil {
		return nil, err
	}
	cmd := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", item, "-w")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("securestore: keychain read failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

// Delete removes an item.
func (k *Keychain) Delete(item string) error {
	if err := checkName("item", item); err != nil {
		return err
	}
	cmd := exec.Command("security", "delete-generic-password", "-s", k.Service, "-a", item)
	cmd.Stdout, cmd.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return ErrNotFound
		}
		return fmt.Errorf("securestore: keychain delete failed: %v", err)
	}
	return nil
}

func platformStore(service string) (Store, bool) {
	k := NewKeychain(service)
	return k, k.Available()
}
