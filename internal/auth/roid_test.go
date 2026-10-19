package auth

import (
	"errors"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/securestore"
)

func roidConfig() *config.Config {
	c := &config.Config{AuthIssuer: "https://id.example.com", ConductorURL: "https://api.example.com"}
	c.ApplyRoidLogin("https://id.example.com", "p_alice", "acct1", "2026-10-15T00:00:00Z")
	return c
}

func TestKindRanksTheRoidSignInBelowPATsAndAboveFirebase(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	c := roidConfig()
	if got := Kind(c); got != CredentialRoid || got.IsPAT() {
		t.Fatalf("roid sign-in kind = %q (PAT %v), want %q and not a PAT", got, got.IsPAT(), CredentialRoid)
	}
	c.Firebase = &config.FirebaseConfig{APIKey: "fb"}
	if got := Kind(c); got != CredentialRoid {
		t.Fatalf("with a Firebase config too, kind = %q", got)
	}
	c.APIKey = "pat"
	if got := Kind(c); got != CredentialStoredPAT {
		t.Fatalf("a stored PAT must win, kind = %q", got)
	}
	t.Setenv(APIKeyEnvVar, "pat-env")
	if got := Kind(c); got != CredentialEnvPAT {
		t.Fatalf("the env PAT must win, kind = %q", got)
	}
}

func TestARoidSignInForAnotherIssuerIsNotUsed(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	c := roidConfig()
	c.AuthIssuer = "https://id.other.example.com"
	if got := Kind(c); got == CredentialRoid {
		t.Fatal("a sign-in to another issuer was picked")
	}
	if HasCredentials(c) {
		t.Fatal("a sign-in to another issuer counted as present")
	}
}

type emptyStore struct{}

func (emptyStore) Get(string) ([]byte, error) { return nil, securestore.ErrNotFound }
func (emptyStore) Set(string, []byte) error   { return nil }
func (emptyStore) Delete(string) error        { return securestore.ErrNotFound }
func (emptyStore) Kind() string               { return "empty" }

func TestResolveTokenWithAMissingStoredSecretSaysNotAuthenticated(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	restore := openRoidStore
	openRoidStore = func() (securestore.Store, error) { return emptyStore{}, nil }
	t.Cleanup(func() { openRoidStore = restore; resetRoidSessions() })
	_, err := ResolveToken(roidConfig())
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("err = %v, want ErrNotAuthenticated", err)
	}
}

func TestResolveTokenReportsANoSecureStoreHostPlainly(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	restore := openRoidStore
	openRoidStore = func() (securestore.Store, error) {
		return nil, &securestore.NoSecureStoreError{Reason: "no OS credential store on this host"}
	}
	t.Cleanup(func() { openRoidStore = restore; resetRoidSessions() })
	_, err := ResolveToken(roidConfig())
	var nss *securestore.NoSecureStoreError
	if !errors.As(err, &nss) {
		t.Fatalf("err = %v, want the no-secure-store error, not a sign-out", err)
	}
}
