package vpn

import (
	"os"
	"testing"
	"time"
)

/*
The state file survives an update and a rollback of the program.

The daemon keeps the device key and the session in state.json, and an update replaces the program
that reads it. Resume depends on the session in that file, so a file the new build cannot read, or
reads without the session, turns an update into a forced sign-in. The schema version has stayed at
1 across releases; these pin that a file with the fields of an earlier release loads with its
session intact, and that a file written by a LATER build (extra fields) still loads, which is what
a rollback meets. Fixture values are placeholders.
*/
func TestStateFromAnEarlierReleaseKeepsItsLiveSession(t *testing.T) {
	dir := t.TempDir()
	expires := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := `{
  "schemaVersion": 1,
  "activeAccountId": "acct",
  "accounts": {
    "acct": {
      "accountId": "acct",
      "privateKey": "AAAA",
      "publicKey": "BBBB",
      "deviceId": "device-1",
      "conductorUrl": "https://api.example.test",
      "sessionToken": "session",
      "sessionExpiresAt": "` + expires + `",
      "enrolled": true
    }
  }
}`
	if err := os.WriteFile(statePath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := LoadState(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	active := state.Active()
	if active == nil || active.SessionToken != "session" || !active.SessionExpiresAt.After(time.Now()) {
		t.Fatalf("active = %+v, want the stored live session", active)
	}
	if active.DeviceID != "device-1" || active.ConductorURL != "https://api.example.test" {
		t.Errorf("device or conductor lost: %+v", active)
	}
}

func TestStateWithFieldsFromALaterBuildStillLoadsAfterARollback(t *testing.T) {
	dir := t.TempDir()
	body := `{
  "schemaVersion": 1,
  "activeAccountId": "acct",
  "someFutureField": {"a": 1},
  "accounts": {
    "acct": {
      "accountId": "acct", "privateKey": "AAAA", "publicKey": "BBBB",
      "sessionToken": "session", "sessionExpiresAt": "2099-01-01T00:00:00Z",
      "enrolled": true, "aFutureAccountField": true
    }
  }
}`
	if err := os.WriteFile(statePath(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(dir)
	if err != nil {
		t.Fatalf("a rollback must read a newer state file: %v", err)
	}
	if state.Active() == nil || state.Active().SessionToken != "session" {
		t.Fatalf("session lost across a rollback: %+v", state.Active())
	}
}
