package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"
	"github.com/runos-official/cli/internal/securestore"
)

/*
A roid sign-in that the environment no longer names (F4).

Removing the auth block from the CDN config does not end the sign-in of a person who already has
one: `runos config env <name>` clears the environment's issuer and leaves the sign-in record and its
secrets where they are, so switching back restores it. What must hold is that every path that ENDS a
sign-in (`runos logout`, a Firebase sign-in, a PAT login) revokes the recorded roid sign-in against
the issuer it was made with, whether or not the environment names that issuer today.
*/

const leftoverPerson = "p_alice"

// leftoverIssuer is a stand-in identity service that records the revokes it receives.
type leftoverIssuer struct {
	server *httptest.Server
	mu     sync.Mutex
	logout int
}

func newLeftoverIssuer(t *testing.T) *leftoverIssuer {
	t.Helper()
	li := &leftoverIssuer{}
	li.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		li.mu.Lock()
		defer li.mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/cli/logout" {
			li.logout++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(li.server.Close)
	return li
}

func (li *leftoverIssuer) revokes() int {
	li.mu.Lock()
	defer li.mu.Unlock()
	return li.logout
}

// seedLeftoverSignIn writes a config that records a roid sign-in for `issuer` while the
// environment names NO issuer, and puts that sign-in's refresh token in the encrypted credential
// file. It returns the store so the test can see whether the secret is still there.
func seedLeftoverSignIn(t *testing.T, issuer string) securestore.Store {
	t.Helper()
	t.Setenv("RUNOS_CREDENTIAL_PASSPHRASE", "test-only-passphrase")
	home := os.Getenv("HOME")
	cfgPath := filepath.Join(home, ".runos", "config.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["roid"] = map[string]string{"issuer": issuer, "person_id": leftoverPerson}
	out, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := securestore.Options{
		Service: roidauth.StoreService, AllowEncryptedFile: true, Passphrase: []byte("test-only-passphrase"),
		FilePath: filepath.Join(home, ".runos", "roid-credentials.enc"),
	}
	store, err := securestore.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	// The item names are roidauth's: a hash of the issuer, then the person.
	sum := sha256.Sum256([]byte(issuer))
	base := hex.EncodeToString(sum[:6]) + "." + leftoverPerson
	if err := store.Set(base+".refresh", []byte("rt_test")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(base+".key", []byte("seed")); err != nil {
		t.Fatal(err)
	}
	seeded := leftoverStore{opts: opts, refresh: base + ".refresh"}
	if !seeded.refreshStillThere() {
		t.Fatal("test setup: the seeded refresh token is not readable from a fresh handle")
	}
	return seeded
}

// leftoverStore reads the credential file afresh each time, as another process would.
type leftoverStore struct {
	securestore.Store
	opts    securestore.Options
	refresh string
}

func (s leftoverStore) refreshStillThere() bool {
	store, err := securestore.Open(s.opts)
	if err != nil {
		return false
	}
	_, err = store.Get(s.refresh)
	return err == nil
}

func TestLogoutRevokesARoidSignInTheEnvironmentNoLongerNames(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	writeConfig(t, conductor.server.URL, "aaaaa", false)
	issuer := newLeftoverIssuer(t)
	store := seedLeftoverSignIn(t, issuer.server.URL).(leftoverStore)

	if _, err := runLogoutCommand(t, daemon.path); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if issuer.revokes() != 1 {
		t.Fatalf("the issuer saw %d revokes; the recorded sign-in stayed live server side", issuer.revokes())
	}
	if store.refreshStillThere() {
		t.Fatal("the refresh token stayed in the credential store after logout")
	}
}

func TestAFirebaseSignInRevokesALeftoverRoidSignIn(t *testing.T) {
	conductor := newFakeConductor(t)
	daemon := newFakeDaemon(t)
	fakeFirebase(t)
	writeConfig(t, conductor.server.URL, "aaaaa", false)
	issuer := newLeftoverIssuer(t)
	store := seedLeftoverSignIn(t, issuer.server.URL).(leftoverStore)

	if err := runLogin(newLoginTestCommand(daemon.path), nil); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := readConfig(t)["roid"]; got != nil {
		t.Fatalf("the roid record survived a Firebase sign-in: %v", got)
	}
	if issuer.revokes() != 1 {
		t.Fatalf("the issuer saw %d revokes; a Firebase sign-in left the roid sign-in live", issuer.revokes())
	}
	if store.refreshStillThere() {
		t.Fatal("the roid refresh token stayed in the credential store after a Firebase sign-in")
	}
}

func TestConfigEnvNamesALeftoverRoidSignIn(t *testing.T) {
	left := &config.Config{Roid: &config.RoidSignIn{Issuer: "https://id.example.com", PersonID: "p_alice"}}
	note := leftoverRoidNote(left)
	for _, want := range []string{"https://id.example.com", "runos logout"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q does not say %q", note, want)
		}
	}
	live := &config.Config{AuthIssuer: "https://id.example.com/", Roid: left.Roid}
	for name, cfg := range map[string]*config.Config{
		"no record":          {},
		"sign-in still used": live,
	} {
		if got := leftoverRoidNote(cfg); got != "" {
			t.Errorf("%s: note = %q, want none", name, got)
		}
	}
}
