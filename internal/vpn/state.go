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

// The daemon's on-disk state, root-owned and 0600. It holds the one secret the CLI must never
// see (the device private key) plus the session token and the last document, so a daemon restart
// or a machine reboot resumes without a new sign-in until the session lapses on its own.
//
// This is NOT the user's ~/.runos/config.json: under launchd the daemon runs as root, whose home
// is /var/root, so the two are deliberately separate files. Conductor's URL, account and device
// arrive from the CLI over the socket (OpUp), never read from the user's config by the daemon.

// State is the persisted daemon state.
type State struct {
	// The device keypair. The private key is base64 (WireGuard's own encoding); it never leaves
	// this file or the daemon process.
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
	// Enrolment, learned at the first successful up.
	DeviceID     string `json:"deviceId,omitempty"`
	AccountID    string `json:"accountId,omitempty"`
	ConductorURL string `json:"conductorUrl,omitempty"`
	// The live session. Empty token means signed out.
	SessionToken     string    `json:"sessionToken,omitempty"`
	SessionExpiresAt time.Time `json:"sessionExpiresAt,omitempty"`
}

// PrivateKeyHex returns the device private key as the UAPI hex, or an error when it is not a
// 32-byte base64 key.
func (s *State) PrivateKeyHex() (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("device private key is not base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("device private key is %d bytes, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// statePath is the state file inside a state dir.
func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// LoadState reads the state from dir, generating a fresh device keypair and writing it when no
// state exists yet: a daemon always has an identity, even before its first sign-in.
func LoadState(dir string) (*State, error) {
	raw, err := os.ReadFile(statePath(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return newStateWithKey(dir)
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &state, nil
}

// SaveState writes the state atomically, 0600, creating the dir 0700.
func SaveState(dir string, state *State) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
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

// ClearIdentity forgets the enrolment and session but KEEPS the device key, for `logout`: the
// key is the device's identity and the same machine keeps it until the device row is revoked.
func (s *State) ClearIdentity() {
	s.DeviceID = ""
	s.AccountID = ""
	s.SessionToken = ""
	s.SessionExpiresAt = time.Time{}
}

// RotateKey replaces the device keypair and forgets the enrolment and session that belonged to
// the old key: a revoked key can never enrol again, so the machine needs a new identity.
func (s *State) RotateKey() error {
	priv, pub, err := generateKeypair()
	if err != nil {
		return err
	}
	s.PrivateKey = priv
	s.PublicKey = pub
	s.ClearIdentity()
	return nil
}

// ClearSession forgets only the session, for `down`.
func (s *State) ClearSession() {
	s.SessionToken = ""
	s.SessionExpiresAt = time.Time{}
}

func newStateWithKey(dir string) (*State, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return nil, err
	}
	state := &State{PrivateKey: priv, PublicKey: pub}
	if err := SaveState(dir, state); err != nil {
		return nil, err
	}
	return state, nil
}

// generateKeypair makes a Curve25519 keypair in WireGuard's clamped form, base64-encoded. The
// clamping matches `wg genkey`; wireguard-go clamps again on use, so either form interoperates.
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
