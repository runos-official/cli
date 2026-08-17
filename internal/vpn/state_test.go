package vpn

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadStateMigratesEnrolledIdentityIntoAccount(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"privateKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","publicKey":"pub","deviceId":"dev-1","accountId":"acct","conductorUrl":"https://api.example","sessionToken":"tok","sessionExpiresAt":"2030-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity := state.Accounts["acct"]
	if state.SchemaVersion != 1 || state.ActiveAccountID != "acct" || identity == nil || !identity.Enrolled {
		t.Fatalf("migrated state = %+v", state)
	}
}

func TestLoadStateKeepsUnenrolledKeyUnassigned(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"privateKey":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","publicKey":"pub"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Unassigned == nil || state.Unassigned.PublicKey != "pub" || len(state.Accounts) != 0 {
		t.Fatalf("migrated state = %+v", state)
	}
}

func TestAccountKeysRemainStableAcrossSwitches(t *testing.T) {
	state := &State{SchemaVersion: 1, Accounts: map[string]*AccountState{}}
	first, err := state.IdentityForAccount("first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.IdentityForAccount("second")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := state.IdentityForAccount("first")
	if first.PublicKey != again.PublicKey || first.PublicKey == second.PublicKey {
		t.Fatalf("keys are not stable and isolated")
	}
}

func TestForgetAccountDoesNotRemoveOtherIdentity(t *testing.T) {
	state := &State{SchemaVersion: 1, Accounts: map[string]*AccountState{}}
	first, _ := state.IdentityForAccount("first")
	second, _ := state.IdentityForAccount("second")
	first.SessionToken = "one"
	first.SessionExpiresAt = time.Now().Add(time.Hour)
	second.SessionToken = "two"
	state.ActiveAccountID = "first"
	if !state.ForgetAccount("first") || state.Accounts["second"] != second || second.SessionToken != "two" {
		t.Fatalf("forget changed another account: %+v", state)
	}
}
