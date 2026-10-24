//go:build darwin

package securestore

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// testService returns a throwaway service name. Every item the tests
// create lives under it, and cleanup deletes every one.
func testService(t *testing.T) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "runos-cli-test-" + hex.EncodeToString(b)
}

// requireRealKeychain skips a test that writes to the login keychain unless
// RUNOS_TEST_KEYCHAIN=1. CI hosts have no unlocked keychain, and a developer
// machine may prompt for each new item.
func requireRealKeychain(t *testing.T) {
	t.Helper()
	if os.Getenv("RUNOS_TEST_KEYCHAIN") != "1" {
		t.Skip("set RUNOS_TEST_KEYCHAIN=1 to run tests that write to the login keychain")
	}
}

func cleanup(t *testing.T, k *Keychain, items ...string) {
	t.Cleanup(func() {
		for _, it := range items {
			_ = k.Delete(it)
			out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", it).CombinedOutput()
			if err == nil {
				t.Errorf("item %s/%s still in the keychain after cleanup: %s", k.Service, it, out)
			}
		}
	})
}

func TestKeychainRoundTripOnThisHost(t *testing.T) {
	requireRealKeychain(t)
	k := NewKeychain(testService(t))
	if !k.Available() {
		t.Fatal("macOS keychain not available on this darwin host")
	}
	cleanup(t, k, "acct-a.refresh")
	secret := []byte("rt_" + strings.Repeat("s3cr3t", 8))
	if err := k.Set("acct-a.refresh", secret); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Read back through the security tool directly, not only through our code.
	out, err := exec.Command("security", "find-generic-password", "-s", k.Service, "-a", "acct-a.refresh", "-w").Output()
	if err != nil {
		t.Fatalf("item not visible to security(1): %v", err)
	}
	if strings.TrimSpace(string(out)) != base64.StdEncoding.EncodeToString(secret) {
		t.Fatalf("stored value %q", out)
	}
	got, err := k.Get("acct-a.refresh")
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := k.Set("acct-a.refresh", []byte("rotated")); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := k.Get("acct-a.refresh"); string(got) != "rotated" {
		t.Fatalf("after update %q", got)
	}
	if err := k.Delete("acct-a.refresh"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("acct-a.refresh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := k.Delete("acct-a.refresh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestKeychainSecretNeverOnTheCommandLine(t *testing.T) {
	k := NewKeychain("runos-cli-test-argv")
	secret := []byte("very-secret-value")
	argv, stdin, err := k.addCommand("acct-a.refresh", secret)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(secret)
	for _, a := range argv {
		if strings.Contains(a, enc) || strings.Contains(a, string(secret)) {
			t.Fatalf("secret on argv: %q", argv)
		}
	}
	if !strings.Contains(stdin, enc) {
		t.Fatal("secret not on stdin")
	}
}

func TestKeychainRefusesUnsafeNames(t *testing.T) {
	k := NewKeychain("runos-cli-test-names")
	for _, it := range []string{`a"b`, "a\nb", "a b", "", `a\b`} {
		if _, _, err := k.addCommand(it, []byte("x")); err == nil {
			t.Errorf("item %q accepted", it)
		}
	}
	if _, _, err := NewKeychain(`svc"x`).addCommand("ok", []byte("x")); err == nil {
		t.Error("unsafe service accepted")
	}
}

func TestOpenPicksTheKeychainOnThisMac(t *testing.T) {
	requireRealKeychain(t)
	s, err := Open(Options{Service: testService(t)})
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind() != "macos-keychain" {
		t.Fatalf("kind %q", s.Kind())
	}
}

// The login keychain under the CLI's real service name is off limits to a test binary, even
// through the backend directly. Each call must refuse before security(1) runs.
func TestKeychainRefusesTheRealServiceInATestBinary(t *testing.T) {
	t.Setenv("RUNOS_TEST_KEYCHAIN", "1")
	k := NewKeychain("runos-cli-roid")
	var nss *NoSecureStoreError
	if _, err := k.Get("probe"); !errors.As(err, &nss) || !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("Get under the real service = %v, want the test-binary refusal", err)
	}
	if err := k.Set("probe", []byte("x")); !errors.As(err, &nss) {
		t.Fatalf("Set under the real service = %v, want the test-binary refusal", err)
	}
	if err := k.Delete("probe"); !errors.As(err, &nss) {
		t.Fatalf("Delete under the real service = %v, want the test-binary refusal", err)
	}
}
