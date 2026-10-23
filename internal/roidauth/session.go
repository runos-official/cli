package roidauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/runos-official/cli/internal/securestore"
)

// StoreService is the OS credential store service name for roid secrets.
const StoreService = "runos-cli-roid"

// ErrNotSignedIn means this install holds no roid sign-in.
var ErrNotSignedIn = errors.New("not signed in: run 'runos login'")

// ErrSessionEnded means roid ended the sign-in: logged out elsewhere,
// revoked by an admin, past its maximum age, or a spent refresh token was
// presented again and roid revoked the whole family.
var ErrSessionEnded = errors.New("your sign-in has ended: run 'runos login' to sign in again")

// accessSkew renews an access token this long before roid would refuse it.
const accessSkew = 30 * time.Second

var personIDShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// Session is one install's sign-in to one roid issuer. The refresh token
// and the key seed live only in Store. The access token lives only in
// memory, for the life of the process.
type Session struct {
	Issuer string
	// Person is the signed-in person id, from the CLI config. Empty before
	// Login and after Logout.
	Person string
	Store  securestore.Store
	// LockPath orders refreshes across CLI processes. Without it two
	// processes that read the same refresh token revoke their own family.
	LockPath string
	Client   *Client
	// ProofHosts are the hosts (host:port) the proof transport may send
	// access proofs to: the RunOS API and the issuer.
	ProofHosts []string
	// OnPending runs after each authorization_pending poll during Login.
	OnPending func()

	mu        sync.Mutex
	access    string
	accessExp time.Time
	key       *Key
}

func (s *Session) now() time.Time {
	if s.Client != nil && s.Client.Now != nil {
		return s.Client.Now()
	}
	return time.Now()
}

// itemBase names this session's store items. The issuer is part of the
// name, so a sign-in to one environment never answers for another.
func (s *Session) itemBase() string {
	h := sha256.Sum256([]byte(s.Issuer))
	return hex.EncodeToString(h[:6]) + "." + s.Person
}

func (s *Session) refreshItem() string { return s.itemBase() + ".refresh" }
func (s *Session) keyItem() string     { return s.itemBase() + ".key" }

// remember caches an access token and registers it with the proof
// transport. The caller holds s.mu.
func (s *Session) remember(t Tokens, key *Key) {
	s.access, s.key = t.AccessToken, key
	s.accessExp = s.now().Add(time.Duration(t.ExpiresIn) * time.Second)
	defaultRegistry.add(t.AccessToken, key, s.accessExp, s.ProofHosts...)
}

// Login runs the device flow with a fresh key, stores the result and
// returns who signed in. show gets the code and link to put in front of
// the person.
func (s *Session) Login(ctx context.Context, show func(DeviceStart)) (Identity, error) {
	if s.Store == nil {
		return Identity{}, &securestore.NoSecureStoreError{Reason: "no secure store"}
	}
	key, err := NewKey()
	if err != nil {
		return Identity{}, err
	}
	start, err := s.Client.StartDevice(key)
	if err != nil {
		return Identity{}, fmt.Errorf("could not start the sign-in: %w", err)
	}
	if show != nil {
		show(start)
	}
	tokens, err := s.Client.PollDevice(ctx, start, key, s.OnPending)
	if err != nil {
		return Identity{}, err
	}
	id, err := s.Client.identity(tokens.AccessToken, key)
	if err != nil {
		return Identity{}, fmt.Errorf("signed in, but could not read who you are: %w", err)
	}
	if !personIDShape.MatchString(id.PersonID) {
		return Identity{}, fmt.Errorf("the identity service returned an unexpected person id")
	}
	unlock, err := lockFile(s.LockPath)
	if err != nil {
		return Identity{}, err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Person = id.PersonID
	if err := s.Store.Set(s.refreshItem(), []byte(tokens.RefreshToken)); err != nil {
		return Identity{}, err
	}
	if err := s.Store.Set(s.keyItem(), key.Seed()); err != nil {
		_ = s.Store.Delete(s.refreshItem())
		return Identity{}, err
	}
	s.remember(tokens, key)
	return id, nil
}

// CaptureRefresh returns a copy of the stored refresh token of this sign-in, or nil when there is
// none. It changes nothing. A caller that is about to replace this sign-in reads it first: the new
// sign-in of the same person writes to the same store slots, so the old token is gone from the
// store the moment the new one is saved, and the server still holds its family.
func (s *Session) CaptureRefresh() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Person == "" || s.Store == nil {
		return nil
	}
	rt, err := s.Store.Get(s.refreshItem())
	if err != nil || len(rt) == 0 {
		return nil
	}
	return rt
}

// Release forgets this session in memory only: the access token, the key and the person. It never
// touches the store or the server. A caller uses it when another sign-in now owns the stored
// secrets, so a later lookup builds a new session from them instead of answering with this one.
func (s *Session) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	defaultRegistry.remove(s.access)
	s.access, s.key, s.accessExp, s.Person = "", nil, time.Time{}, ""
}

// RevokeCaptured revokes on the server the token family of a refresh token read earlier with
// CaptureRefresh. It changes no local secret. The server's answer is returned so the caller can
// say that the revoke was not confirmed.
func (s *Session) RevokeCaptured(token []byte) error {
	if len(token) == 0 || s.Client == nil {
		return nil
	}
	if err := s.Client.Logout(string(token)); err != nil {
		return fmt.Errorf("the previous sign-in could not be revoked on the server: %w", err)
	}
	return nil
}

// SignedInPerson is the person this session signs in as, empty after Logout or before a sign-in.
func (s *Session) SignedInPerson() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Person
}

// Bearer returns an access token to send. It reuses the process's token
// while it is fresh; otherwise it spends the stored refresh token under
// the lock and stores the replacement.
func (s *Session) Bearer() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.access != "" && s.now().Add(accessSkew).Before(s.accessExp) {
		return s.access, nil
	}
	if s.Person == "" || s.Store == nil {
		return "", ErrNotSignedIn
	}
	unlock, err := lockFile(s.LockPath)
	if err != nil {
		return "", err
	}
	defer unlock()
	rt, err := s.Store.Get(s.refreshItem())
	if errors.Is(err, securestore.ErrNotFound) {
		return "", ErrNotSignedIn
	}
	if err != nil {
		return "", fmt.Errorf("could not read your sign-in from the credential store: %w", err)
	}
	seed, err := s.Store.Get(s.keyItem())
	if err != nil {
		return "", fmt.Errorf("could not read your sign-in key from the credential store: %w", err)
	}
	key, err := KeyFromSeed(seed)
	if err != nil {
		return "", err
	}
	tokens, err := s.Client.Refresh(string(rt), key)
	if err != nil {
		var oe *Error
		if errors.As(err, &oe) && (oe.Code == "invalid_grant" || oe.Code == "invalid_token") {
			// The family is gone on the server. Keeping the spent secret
			// would only make every later command fail the same way.
			_ = s.Store.Delete(s.refreshItem())
			_ = s.Store.Delete(s.keyItem())
			return "", fmt.Errorf("%w (%s)", ErrSessionEnded, oe.Description)
		}
		return "", err
	}
	// If this write fails the rotated token is lost and the person signs
	// in again. That is safe: the old token is already spent.
	if err := s.Store.Set(s.refreshItem(), []byte(tokens.RefreshToken)); err != nil {
		return "", err
	}
	s.remember(tokens, key)
	return tokens.AccessToken, nil
}

// Identity reads who this session signs in as.
func (s *Session) Identity() (Identity, error) {
	access, err := s.Bearer()
	if err != nil {
		return Identity{}, err
	}
	s.mu.Lock()
	key := s.key
	s.mu.Unlock()
	return s.Client.identity(access, key)
}

// Logout revokes the token family on the server, then deletes the local
// secrets whatever the server said. A failed server revoke is returned so
// the command can say the server did not confirm it.
func (s *Session) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Person == "" || s.Store == nil {
		return nil
	}
	unlock, err := lockFile(s.LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	var serverErr error
	if rt, err := s.Store.Get(s.refreshItem()); err == nil {
		serverErr = s.Client.Logout(string(rt))
	}
	for _, it := range []string{s.refreshItem(), s.keyItem()} {
		if err := s.Store.Delete(it); err != nil && !errors.Is(err, securestore.ErrNotFound) {
			return err
		}
	}
	defaultRegistry.remove(s.access)
	s.access, s.key, s.accessExp, s.Person = "", nil, time.Time{}, ""
	if serverErr != nil {
		return fmt.Errorf("signed out on this machine, but the server did not confirm the revoke: %w", serverErr)
	}
	return nil
}
