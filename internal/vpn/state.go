package vpn

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/curve25519"
)

const StateSchemaVersion = 1

// AccountState stores one account VPN identity and its current device session.
type AccountState struct {
	AccountID        string    `json:"accountId"`
	PrivateKey       string    `json:"privateKey"`
	PublicKey        string    `json:"publicKey"`
	DeviceID         string    `json:"deviceId,omitempty"`
	ConductorURL     string    `json:"conductorUrl,omitempty"`
	SessionToken     string    `json:"sessionToken,omitempty"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
	Enrolled         bool      `json:"enrolled"`
}

// State is the versioned, root-owned daemon account keyring.
type State struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	ActiveAccountID string                   `json:"activeAccountId,omitempty"`
	Accounts        map[string]*AccountState `json:"accounts"`
	Unassigned      *AccountState            `json:"unassignedIdentity,omitempty"`
}

type legacyState struct {
	PrivateKey       string    `json:"privateKey"`
	PublicKey        string    `json:"publicKey"`
	DeviceID         string    `json:"deviceId,omitempty"`
	AccountID        string    `json:"accountId,omitempty"`
	ConductorURL     string    `json:"conductorUrl,omitempty"`
	SessionToken     string    `json:"sessionToken,omitempty"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
}

func (a *AccountState) PrivateKeyHex() (string, error) {
	if a == nil {
		return "", fmt.Errorf("no active VPN identity")
	}
	raw, err := base64.StdEncoding.DecodeString(a.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("device private key is not base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("device private key is %d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func (a *AccountState) ClearSession() {
	a.SessionToken = ""
	a.SessionExpiresAt = time.Time{}
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// LoadState reads state and migrates every legacy single-identity shape.
func LoadState(dir string) (*State, error) {
	raw, err := os.ReadFile(statePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return newStateWithKey(dir)
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if probe.SchemaVersion == StateSchemaVersion {
		var state State
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
		if state.Accounts == nil {
			state.Accounts = map[string]*AccountState{}
		}
		return &state, nil
	}
	var old legacyState
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("parse legacy state: %w", err)
	}
	identity := &AccountState{
		AccountID: old.AccountID, PrivateKey: old.PrivateKey, PublicKey: old.PublicKey,
		DeviceID: old.DeviceID, ConductorURL: old.ConductorURL, SessionToken: old.SessionToken,
		SessionExpiresAt: old.SessionExpiresAt, Enrolled: old.DeviceID != "",
	}
	state := &State{SchemaVersion: StateSchemaVersion, Accounts: map[string]*AccountState{}}
	if old.AccountID != "" && old.DeviceID != "" {
		state.Accounts[old.AccountID] = identity
		state.ActiveAccountID = old.AccountID
	} else {
		state.Unassigned = identity
	}
	if state.Unassigned != nil && state.Unassigned.PrivateKey == "" {
		var keyErr error
		state.Unassigned, keyErr = newAccountState("")
		if keyErr != nil {
			return nil, keyErr
		}
	}
	if err := SaveState(dir, state); err != nil {
		return nil, err
	}
	return state, nil
}

func SaveState(dir string, state *State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	state.SchemaVersion = StateSchemaVersion
	if state.Accounts == nil {
		state.Accounts = map[string]*AccountState{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	tmp := statePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, statePath(dir)); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}

func (s *State) Active() *AccountState {
	if s == nil || s.ActiveAccountID == "" {
		return nil
	}
	return s.Accounts[s.ActiveAccountID]
}

/*
ExistingIdentityForAccount returns the account's key, or nil when it has never had one.

The difference from IdentityForAccount is the whole point: that one MINTS a key when it finds none,
which is right when the CLI is about to enrol whatever it gets back, and wrong everywhere else. On
an account switch the CLI enrolled the previous account's key and then handed the daemon the new
account id, which minted a second keypair, brought the tunnel up on it, and routed nothing: the
public key conductor held was not the private key the tunnel was using.

A caller that cannot enrol must use this one and refuse.
*/
func (s *State) ExistingIdentityForAccount(accountID string) *AccountState {
	if s == nil {
		return nil
	}
	if accountID == "" {
		return s.Active()
	}
	return s.Accounts[accountID]
}

// IdentityForAccount returns a stable key for one account, MINTING one when the account has none.
// Only a caller that will enrol the key it gets back may use this; see ExistingIdentityForAccount.
func (s *State) IdentityForAccount(accountID string) (*AccountState, error) {
	if accountID == "" {
		if active := s.Active(); active != nil {
			return active, nil
		}
		if s.Unassigned != nil {
			return s.Unassigned, nil
		}
		return nil, fmt.Errorf("account ID is required")
	}
	if identity := s.Accounts[accountID]; identity != nil {
		return identity, nil
	}
	var identity *AccountState
	if s.Unassigned != nil {
		identity = s.Unassigned
		s.Unassigned = nil
		identity.AccountID = accountID
	} else {
		var err error
		identity, err = newAccountState(accountID)
		if err != nil {
			return nil, err
		}
	}
	s.Accounts[accountID] = identity
	return identity, nil
}

func (s *State) RotateAccountKey(accountID string) (*AccountState, error) {
	identity, err := newAccountState(accountID)
	if err != nil {
		return nil, err
	}
	s.Accounts[accountID] = identity
	if s.ActiveAccountID == accountID {
		s.ActiveAccountID = ""
	}
	return identity, nil
}

func (s *State) ForgetAccount(accountID string) bool {
	if _, ok := s.Accounts[accountID]; !ok {
		return false
	}
	delete(s.Accounts, accountID)
	if s.ActiveAccountID == accountID {
		s.ActiveAccountID = ""
	}
	return true
}

func newStateWithKey(dir string) (*State, error) {
	identity, err := newAccountState("")
	if err != nil {
		return nil, err
	}
	state := &State{SchemaVersion: StateSchemaVersion, Accounts: map[string]*AccountState{}, Unassigned: identity}
	if err := SaveState(dir, state); err != nil {
		return nil, err
	}
	return state, nil
}

func newAccountState(accountID string) (*AccountState, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return nil, err
	}
	return &AccountState{AccountID: accountID, PrivateKey: priv, PublicKey: pub}, nil
}

func generateKeypair() (privB64, pubB64 string, err error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", fmt.Errorf("read random: %w", err)
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv[:]), base64.StdEncoding.EncodeToString(pub), nil
}
