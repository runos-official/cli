package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// stubSignIn records what was done to a sign-in, in order.
type stubSignIn struct {
	person    string
	calls     []string
	revokeErr error
	logoutErr error
}

func (s *stubSignIn) SignedInPerson() string { return s.person }
func (s *stubSignIn) Release()               { s.calls = append(s.calls, "release") }
func (s *stubSignIn) RevokeCaptured(token []byte) error {
	s.calls = append(s.calls, "revoke:"+string(token))
	return s.revokeErr
}
func (s *stubSignIn) Logout() error {
	s.calls = append(s.calls, "logout")
	return s.logoutErr
}

func (s *stubSignIn) did() string { return strings.Join(s.calls, ",") }

// The sign-in a new one replaces stays live until the new one is approved, stored and saved. Only
// then is it retired, and how depends on whether the new sign-in landed in the same store slots.

func TestTheSamePersonOnTheSameIssuerRetiresTheOldTokenWithoutTouchingTheStoredSecrets(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}
	var warn bytes.Buffer

	held.retire("p_alice", "https://id.example.test", &warn)

	// The new secrets are in the old slots, so Logout (which deletes the slots) must never run.
	if got := old.did(); got != "release,revoke:rt_old" {
		t.Fatalf("calls %q, want release then revoke of the captured token", got)
	}
	if warn.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warn.String())
	}
}

func TestADifferentPersonRetiresTheOldSignInWithALogout(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}

	held.retire("p_bob", "https://id.example.test", &bytes.Buffer{})

	// Different person, different store slots: the old slots are the old sign-in's alone.
	if got := old.did(); got != "logout" {
		t.Fatalf("calls %q, want logout", got)
	}
}

func TestADifferentIssuerRetiresTheOldSignInWithALogoutEvenForTheSamePerson(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://old.example.test", refresh: []byte("rt_old")}

	held.retire("p_alice", "https://new.example.test", &bytes.Buffer{})

	// The issuer is part of the slot name, so these slots are not the new sign-in's.
	if got := old.did(); got != "logout" {
		t.Fatalf("calls %q, want logout", got)
	}
}

func TestAFailedRevokeOnlyWarns(t *testing.T) {
	old := &stubSignIn{person: "p_alice", revokeErr: errors.New("server down")}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}
	var warn bytes.Buffer

	held.retire("p_alice", "https://id.example.test", &warn)

	if !strings.Contains(warn.String(), "server down") {
		t.Fatalf("warning %q does not carry the reason", warn.String())
	}
}

func TestRetiringNothingIsSafe(t *testing.T) {
	var held *heldSignIn
	held.retire("p_alice", "https://id.example.test", &bytes.Buffer{}) // must not panic
}

// The sign-in check for a VPN session (vpn up) is for the person who is signed in. Approving it as
// someone else is refused: the new sign-in is ended, and the old one and the tunnel stay as they were.

func TestADifferentApproverIsRefusedAndTheNewSignInIsEnded(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	fresh := &stubSignIn{person: "p_bob"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}

	err := refuseDifferentPerson(held, fresh, "p_bob", true, &bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "different account holder") {
		t.Fatalf("error %v, want a plain refusal", err)
	}
	if got := fresh.did(); got != "logout" {
		t.Fatalf("new sign-in calls %q, want logout (it must not stay live)", got)
	}
	if got := old.did(); got != "" {
		t.Fatalf("the old sign-in was touched: %q", got)
	}
}

func TestTheSameApproverIsNotRefused(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	fresh := &stubSignIn{person: "p_alice"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}

	if err := refuseDifferentPerson(held, fresh, "p_alice", true, &bytes.Buffer{}); err != nil {
		t.Fatalf("refused the same person: %v", err)
	}
	if fresh.did() != "" || old.did() != "" {
		t.Fatalf("a refusal check changed something: new %q old %q", fresh.did(), old.did())
	}
}

func TestPlainLoginAllowsADifferentPerson(t *testing.T) {
	old := &stubSignIn{person: "p_alice"}
	fresh := &stubSignIn{person: "p_bob"}
	held := &heldSignIn{session: old, person: "p_alice", issuer: "https://id.example.test", refresh: []byte("rt_old")}

	if err := refuseDifferentPerson(held, fresh, "p_bob", false, &bytes.Buffer{}); err != nil {
		t.Fatalf("`runos login` refused a different person: %v", err)
	}
}

func TestThereIsNothingToCompareWhenNobodyWasSignedIn(t *testing.T) {
	fresh := &stubSignIn{person: "p_bob"}
	if err := refuseDifferentPerson(nil, fresh, "p_bob", true, &bytes.Buffer{}); err != nil {
		t.Fatalf("refused with no previous sign-in: %v", err)
	}
}
