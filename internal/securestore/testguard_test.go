package securestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/testguard"
)

/*
A test binary never opens the real credential store.

The CLI keeps a person's sign-in under the service "runos-cli-roid". A test that opened that service
through the OS store, and then signed out, deleted the developer's real sign-in. Open is the one
opener, so it refuses here. No case below calls Get, Set or Delete on what Open returns, so this test
cannot touch the real keychain even on code that still has the bug.
*/
func TestOpenRefusesTheOSStoreInATestBinary(t *testing.T) {
	s, err := Open(Options{Service: "runos-cli-roid"})
	if err == nil {
		t.Fatalf("Open returned the %s store in a test binary, want a refusal", s.Kind())
	}
	if !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

// The encrypted file is the headless store. In a test binary it is allowed only under the temp dir.
func TestOpenInATestBinaryUsesOnlyATempEncryptedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.enc")
	s, err := Open(Options{Service: "runos-cli-roid", AllowEncryptedFile: true, Passphrase: []byte("p"), FilePath: path})
	if err != nil {
		t.Fatalf("Open with a temp encrypted file: %v", err)
	}
	if s.Kind() != "encrypted-file" {
		t.Fatalf("Open picked %q, want the encrypted file", s.Kind())
	}

	home, _ := os.UserHomeDir()
	real := filepath.Join(home, ".runos", "roid-credentials.enc")
	if testguard.UnderTempDir(real) {
		t.Skip("HOME is under the temp dir; no real path to refuse")
	}
	if _, err := Open(Options{Service: "runos-cli-roid", AllowEncryptedFile: true, Passphrase: []byte("p"), FilePath: real}); err == nil {
		t.Fatal("Open accepted an encrypted file outside the temp dir in a test binary")
	}
}

// An explicit Platform is the test's own fake, and Open uses it.
func TestOpenUsesAnExplicitFakePlatform(t *testing.T) {
	fake := &fakeStore{}
	s, err := Open(Options{Service: "runos-cli-roid", Platform: func(string) (Store, bool) { return fake, true }})
	if err != nil || s != Store(fake) {
		t.Fatalf("Open with a fake platform = %v, %v", s, err)
	}
}

type fakeStore struct{}

func (*fakeStore) Get(string) ([]byte, error) { return nil, ErrNotFound }
func (*fakeStore) Set(string, []byte) error   { return nil }
func (*fakeStore) Delete(string) error        { return ErrNotFound }
func (*fakeStore) Kind() string               { return "fake" }
