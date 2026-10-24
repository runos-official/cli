// Package securestore keeps CLI secrets in the OS credential store. It
// never falls back to a plain file. An encrypted file is allowed only by an
// explicit option with a passphrase, and it prints a warning.
//
// Backends: macOS Keychain through security(1) (darwin, run on a real
// host), Linux Secret Service through secret-tool(1) (linux, built, not
// yet run against a Secret Service), Windows Credential Manager through
// advapi32 (windows, built, not yet run). The roid client library owns the
// reference copy of this package; keep the two in step.
package securestore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// CodeNoSecureStore is the exit code string when no secure store exists.
const CodeNoSecureStore = "cli.no_secure_store"

// ErrNotFound means the item does not exist.
var ErrNotFound = errors.New("securestore: item not found")

// NoSecureStoreError is returned when Open finds no allowed store.
type NoSecureStoreError struct{ Reason string }

func (e *NoSecureStoreError) Error() string { return CodeNoSecureStore + ": " + e.Reason }

// Store holds secrets by item name.
type Store interface {
	Get(item string) ([]byte, error)
	Set(item string, secret []byte) error
	Delete(item string) error
	Kind() string
}

// Options selects a store.
type Options struct {
	Service            string
	AllowEncryptedFile bool
	FilePath           string
	Passphrase         []byte
	Warn               io.Writer
	// Platform overrides the OS store lookup (tests use it to simulate a
	// headless host). nil means the build's OS store.
	Platform func(service string) (Store, bool)
}

// safeName bounds service and item names. They reach a command line (the
// item name, never the secret) or a stdin command script, so quoting
// characters are refused outright.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func checkName(kind, s string) error {
	if !safeName.MatchString(s) {
		return fmt.Errorf("securestore: unsafe %s name %q", kind, s)
	}
	return nil
}

// Open returns the OS store when it is usable. Otherwise it returns the
// encrypted file only when the caller allowed it explicitly and gave a
// passphrase, with a warning. Otherwise it fails with cli.no_secure_store.
// There is no plain-file path.
func Open(o Options) (Store, error) {
	if err := checkName("service", o.Service); err != nil {
		return nil, err
	}
	// A test binary never gets the real OS store or a file outside the temp dir (testguard.go).
	plat := o.Platform
	inTestWithoutFake := plat == nil && !realStoreAllowed(o.Service)
	if plat == nil {
		plat = platformStore
	}
	if inTestWithoutFake {
		plat = absentInTest
	}
	if s, ok := plat(o.Service); ok {
		return s, nil
	}
	if !o.AllowEncryptedFile {
		if inTestWithoutFake {
			return nil, errRealStoreInTest("the real OS credential store is off limits")
		}
		return nil, &NoSecureStoreError{Reason: "no OS credential store on this host; set RUNOS_CREDENTIAL_PASSPHRASE to keep the credentials in an encrypted file, or use a machine key in RUNOS_API_KEY"}
	}
	if len(o.Passphrase) == 0 {
		return nil, &NoSecureStoreError{Reason: "an encrypted credential file needs a passphrase in RUNOS_CREDENTIAL_PASSPHRASE"}
	}
	if o.FilePath == "" {
		return nil, &NoSecureStoreError{Reason: "no encrypted file path"}
	}
	if !encryptedFileAllowed(o.FilePath) {
		return nil, errRealStoreInTest("the encrypted file " + o.FilePath + " is outside the temp dir")
	}
	s, err := NewEncryptedFile(o.FilePath, o.Passphrase)
	if err != nil {
		return nil, err
	}
	if o.Warn != nil {
		fmt.Fprintf(o.Warn, "WARNING: no OS credential store found. Credentials go to an encrypted file at %s. "+
			"Anyone with the file and the passphrase can use them.\n", o.FilePath)
	}
	return s, nil
}

// WriteFileAtomic writes data to a temp file in the same directory, sets
// perm, syncs, renames over path, and syncs the directory. A reader sees
// the old file or the new one, never a partial one. The mode always ends
// as perm, even when path existed with a looser mode (clients inventory
// A1.5: os.WriteFile keeps an existing mode).
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
