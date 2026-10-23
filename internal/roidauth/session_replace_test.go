package roidauth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A new sign-in must not end the current one until the person has approved the new one. A
// sign-in that is cancelled, expires or is killed before approval has to leave the person signed in.

func TestAnUnapprovedReplacementLeavesTheCurrentSignInWorking(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	current := newTestSession(t, f, store)
	login(t, f, current)
	before := len(store.items)

	// A second sign-in that nobody approves: the context ends while it polls.
	replacement := newTestSession(t, f, store)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	f.mu.Lock()
	f.approved = false
	f.mu.Unlock()
	if _, err := replacement.Login(ctx, nil); err == nil {
		t.Fatal("a sign-in nobody approved succeeded")
	}

	if f.logouts != 0 {
		t.Fatalf("the server was asked to revoke the current sign-in %d time(s) by a sign-in that never finished", f.logouts)
	}
	if len(store.items) != before {
		t.Fatalf("store items %d, want %d: an unfinished sign-in changed the stored secrets", len(store.items), before)
	}
	if _, err := current.Bearer(); err != nil {
		t.Fatalf("the current sign-in stopped working: %v", err)
	}
}

func TestReleaseForgetsTheSessionInMemoryAndKeepsTheStoredSecrets(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	s := newTestSession(t, f, store)
	login(t, f, s)
	before := len(store.items)

	s.Release()

	if s.SignedInPerson() != "" {
		t.Fatal("a released session still names a person")
	}
	if len(store.items) != before {
		t.Fatalf("Release changed the store: %d items, want %d", len(store.items), before)
	}
	if f.logouts != 0 {
		t.Fatal("Release called the server")
	}
}

func TestTheOldTokenIsRevokedAfterTheSameCredentialStoreSlotsAreOverwritten(t *testing.T) {
	f := newFakeRoid(t)
	store := newMemStore()
	current := newTestSession(t, f, store)
	login(t, f, current)

	// The token the new sign-in will replace, read before the new sign-in overwrites it. The
	// same person signs in again, so the new secrets land in the same store slots.
	old := current.CaptureRefresh()
	if len(old) == 0 {
		t.Fatal("no refresh token captured from a signed-in session")
	}
	replacement := newTestSession(t, f, store)
	login(t, f, replacement)

	current.Release()
	if err := current.RevokeCaptured(old); err != nil {
		t.Fatalf("revoke the old sign-in: %v", err)
	}

	if f.logouts != 1 {
		t.Fatalf("logouts %d, want exactly 1 for the replaced sign-in", f.logouts)
	}
	// The new sign-in is untouched: the store still holds its secrets, and a fresh session built
	// from the store can use them.
	if len(store.items) != 2 {
		t.Fatalf("store items %d, want the new sign-in's refresh token and key", len(store.items))
	}
	fresh := newTestSession(t, f, store)
	fresh.Person = replacement.SignedInPerson()
	if _, err := fresh.Bearer(); err != nil {
		t.Fatalf("the new sign-in does not work after the old one was revoked: %v", err)
	}
	// And the old family really is gone on the server.
	f.mu.Lock()
	revokedFamilies := 0
	for _, r := range f.revoked {
		if r {
			revokedFamilies++
		}
	}
	f.mu.Unlock()
	if revokedFamilies != 1 {
		t.Fatalf("revoked families %d, want 1", revokedFamilies)
	}
	if strings.TrimSpace(string(old)) == "" {
		t.Fatal("empty captured token")
	}
}
