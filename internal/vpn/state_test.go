package vpn

import (
	"testing"
	"time"
)

// A revoked key can never enrol again (conductor 409 vpn.key_revoked), so RotateKey must give
// the machine a NEW keypair and drop the enrolment and session that belonged to the old one;
// found on dl380p, whose daemon kept a key revoked in an earlier session and was stuck on `up`.
func TestRotateKeyReplacesTheKeypairAndForgetsTheOldIdentity(t *testing.T) {
	dir := t.TempDir()
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldPub, oldPriv := state.PublicKey, state.PrivateKey
	state.DeviceID, state.AccountID = "dev-1", "acct"
	state.SessionToken, state.SessionExpiresAt = "tok", time.Now().Add(time.Hour)

	if err := state.RotateKey(); err != nil {
		t.Fatal(err)
	}
	if state.PublicKey == oldPub || state.PrivateKey == oldPriv || state.PublicKey == "" {
		t.Fatalf("keypair not rotated: %q -> %q", oldPub, state.PublicKey)
	}
	if state.DeviceID != "" || state.AccountID != "" || state.SessionToken != "" || !state.SessionExpiresAt.IsZero() {
		t.Fatalf("old identity survived the rotation: %+v", state)
	}
	if _, err := state.PrivateKeyHex(); err != nil {
		t.Fatalf("new private key is not a valid 32-byte key: %v", err)
	}
}
