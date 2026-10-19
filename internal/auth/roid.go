package auth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/runos-official/cli/internal/config"
	"github.com/runos-official/cli/internal/roidauth"
	"github.com/runos-official/cli/internal/securestore"
)

// PassphraseEnvVar opts a host with no OS credential store into an
// encrypted credential file. Without it such a host cannot hold a roid
// sign-in, and RUNOS_API_KEY is the headless path.
const PassphraseEnvVar = "RUNOS_CREDENTIAL_PASSPHRASE"

// openRoidStore opens the OS credential store for roid secrets. Tests
// replace it.
var openRoidStore = func() (securestore.Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	pass := os.Getenv(PassphraseEnvVar)
	return securestore.Open(securestore.Options{
		Service:            roidauth.StoreService,
		AllowEncryptedFile: pass != "",
		Passphrase:         []byte(pass),
		FilePath:           filepath.Join(dir, "roid-credentials.enc"),
		Warn:               os.Stderr,
	})
}

var (
	roidMu       sync.Mutex
	roidSessions = map[string]*roidauth.Session{}
)

func resetRoidSessions() {
	roidMu.Lock()
	defer roidMu.Unlock()
	roidSessions = map[string]*roidauth.Session{}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// RoidSession returns this process's roid session for the environment's
// issuer, opening the credential store and installing the proof transport
// on first use. The person is the one the config records; it is empty for
// a session that is about to sign in.
func RoidSession(cfg *config.Config) (*roidauth.Session, error) {
	if cfg == nil || cfg.AuthIssuer == "" {
		return nil, errors.New("this environment does not sign in through the RunOS identity service")
	}
	person := ""
	if cfg.HasRoidSignIn() {
		person = cfg.Roid.PersonID
	}
	roidMu.Lock()
	defer roidMu.Unlock()
	cacheKey := cfg.AuthIssuer + "\x00" + person
	if s, ok := roidSessions[cacheKey]; ok {
		return s, nil
	}
	store, err := openRoidStore()
	if err != nil {
		return nil, err
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	roidauth.InstallProofTransport()
	s := &roidauth.Session{
		Issuer:     cfg.AuthIssuer,
		Person:     person,
		Store:      store,
		LockPath:   filepath.Join(dir, "roid.lock"),
		Client:     roidauth.NewClient(cfg.AuthIssuer),
		ProofHosts: []string{hostOf(cfg.GetAPIURL()), hostOf(cfg.AuthIssuer)},
	}
	roidSessions[cacheKey] = s
	return s, nil
}

// roidBearer is the CredentialRoid branch of ResolveToken.
func roidBearer(cfg *config.Config) (string, error) {
	s, err := RoidSession(cfg)
	if err != nil {
		return "", err
	}
	tok, err := s.Bearer()
	if errors.Is(err, roidauth.ErrNotSignedIn) || errors.Is(err, roidauth.ErrSessionEnded) {
		return "", fmt.Errorf("%w: %w", ErrNotAuthenticated, err)
	}
	return tok, err
}
