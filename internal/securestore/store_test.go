package securestore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func headless(string) (Store, bool) { return nil, false }

func TestHeadlessHostRefusesPlainFileFallback(t *testing.T) {
	dir := t.TempDir()
	var warn bytes.Buffer
	_, err := Open(Options{Service: "runos-cli-test-test", FilePath: filepath.Join(dir, "creds.enc"), Warn: &warn, Platform: headless})
	var nss *NoSecureStoreError
	if !errors.As(err, &nss) || !strings.Contains(err.Error(), CodeNoSecureStore) {
		t.Fatalf("headless open gave %v, want %s", err, CodeNoSecureStore)
	}
	// The flag alone is not enough: an encrypted file needs a passphrase.
	_, err = Open(Options{Service: "runos-cli-test-test", FilePath: filepath.Join(dir, "creds.enc"), AllowEncryptedFile: true, Warn: &warn, Platform: headless})
	if !errors.As(err, &nss) {
		t.Fatalf("flag without passphrase gave %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("refusal wrote files: %v", ents)
	}
	if warn.Len() != 0 {
		t.Fatalf("refusal printed a warning instead of failing: %q", warn.String())
	}
}

func TestEncryptedFileNeedsTheFlagAndWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.enc")
	var warn bytes.Buffer
	s, err := Open(Options{Service: "runos-cli-test-test", FilePath: path, AllowEncryptedFile: true,
		Passphrase: []byte("correct horse battery staple"), Warn: &warn, Platform: headless})
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind() != "encrypted-file" || !strings.Contains(warn.String(), "WARNING") {
		t.Fatalf("kind %q warning %q", s.Kind(), warn.String())
	}
	secret := []byte("rt_super_secret_refresh_value")
	if err := s.Set("acct-a.refresh", secret); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("acct-a.refresh")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, secret) {
		t.Fatal("secret in plain text in the encrypted file")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	bad, err := NewEncryptedFile(path, []byte("wrong passphrase"))
	if err == nil {
		if _, err := bad.Get("acct-a.refresh"); err == nil {
			t.Fatal("wrong passphrase decrypted the item")
		}
	}
	if err := s.Delete("acct-a.refresh"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("acct-a.refresh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted item: %v", err)
	}
}

func TestEncryptedFileDetectsTamperAndItemSwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.enc")
	s, err := NewEncryptedFile(path, []byte("pass-pass-pass"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Set("a", []byte("alpha"))
	_ = s.Set("b", []byte("bravo"))
	raw, _ := os.ReadFile(path)
	swapped := bytes.Replace(raw, []byte(`"a":`), []byte(`"x":`), 1)
	swapped = bytes.Replace(swapped, []byte(`"b":`), []byte(`"a":`), 1)
	if err := os.WriteFile(path, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	s2, err := NewEncryptedFile(path, []byte("pass-pass-pass"))
	if err == nil {
		if v, err := s2.Get("a"); err == nil {
			t.Fatalf("item b decrypted under name a: %q", v)
		}
	}
}

func TestWriteFileAtomicReplacesALooserMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after atomic write %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
}

func TestWriteFileAtomicReadersNeverSeeAPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	valid := map[string]bool{}
	payload := func(i int) []byte { return bytes.Repeat([]byte(fmt.Sprintf("%03d", i)), 20000) }
	for i := 0; i < 20; i++ {
		valid[string(payload(i))] = true
	}
	if err := WriteFileAtomic(path, payload(0), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var bad error
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err == nil && !valid[string(b)] {
				mu.Lock()
				bad = fmt.Errorf("partial read of %d bytes", len(b))
				mu.Unlock()
			}
		}
	}()
	var wwg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wwg.Add(1)
		go func(i int) {
			defer wwg.Done()
			if err := WriteFileAtomic(path, payload(i), 0o600); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wwg.Wait()
	close(stop)
	wg.Wait()
	if bad != nil {
		t.Fatal(bad)
	}
}
