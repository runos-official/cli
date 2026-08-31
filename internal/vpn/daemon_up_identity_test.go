package vpn

import (
	"strings"
	"testing"
	"time"
)

/*
An `up` may only use a key this machine ALREADY holds for that account.

THE DEFECT. `handleUpLocked` used to call `IdentityForAccount`, which MINTS a keypair when the
account has none. The CLI enrolled one account's public key, then handed the daemon a different
account id, and this quietly generated a second keypair and started a tunnel with it. Conductor
held the first key; the interface came up; the clusters listed; nothing routed. Nothing about it
looked wrong from either end, which is why it survived.

Refusing is recoverable in one command. Minting was not recoverable at all.

These drive Daemon.Handle directly. A real `up` opens a tun and needs root, but the refusal returns
before any of that, which is precisely the path worth testing.
*/

func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	state, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{stateDir: dir, version: "test", state: state, platform: newPlatform(), pollInterval: PollInterval}
}

func upRequest(account string) Request {
	return Request{
		Op:               OpUp,
		AccountID:        account,
		DeviceID:         "device-1",
		SessionToken:     "session",
		SessionExpiresAt: time.Now().Add(time.Hour),
		ConductorURL:     "http://192.0.2.1:3025",
	}
}

func TestUpRefusesAnAccountThisMachineHasNoKeyFor(t *testing.T) {
	d := newTestDaemon(t)

	resp := d.Handle(upRequest("bbbbb")) // never asked for an identity for this account

	if resp.Error == "" {
		t.Fatal("bringing a tunnel up on a key that was never enrolled must be refused")
	}
	if !strings.Contains(resp.Error, "bbbbb") {
		t.Errorf("must name the account, got %q", resp.Error)
	}
	if !strings.Contains(resp.Error, "runos vpn up") {
		t.Errorf("must name the way out, got %q", resp.Error)
	}
	// THE POINT: no key was invented on the way to refusing.
	if _, minted := d.state.Accounts["bbbbb"]; minted {
		t.Error("a refused up must not leave a keypair behind for that account")
	}
	if d.state.ActiveAccountID == "bbbbb" {
		t.Error("a refused up must not become the active account")
	}
}

// The ordinary sequence. `vpn up` always asks for the identity first, and enrols exactly the key it
// gets back, so by the time OpUp runs the key is there.
func TestUpAcceptsTheAccountWhoseIdentityWasAskedFor(t *testing.T) {
	d := newTestDaemon(t)

	identity := d.Handle(Request{Op: OpIdentity, AccountID: "aaaaa"})
	if identity.Identity == nil || identity.Identity.PublicKey == "" {
		t.Fatalf("identity = %+v, want a key", identity.Identity)
	}

	resp := d.Handle(upRequest("aaaaa"))

	// It gets past the identity check. Starting a real tunnel needs root, so any error here must be
	// about the tunnel, never about a missing key.
	if strings.Contains(resp.Error, "no VPN key enrolled") {
		t.Errorf("the enrolled account must pass the key check, got %q", resp.Error)
	}
	// And the key is the SAME one that was handed out for enrolment: a second one would be the
	// defect arriving by another door.
	if d.state.Accounts["aaaaa"].PublicKey != identity.Identity.PublicKey {
		t.Error("the key used by up must be the key that was enrolled")
	}
}

/*
The lookup itself, which is the difference between the two helpers.

`IdentityForAccount` mints, and only a caller that will ENROL what it gets back may use it.
`ExistingIdentityForAccount` never does. Keeping that distinction visible is the whole fix, so it
has its own test rather than only being reached through Handle.
*/
func TestExistingIdentityNeverMintsAKey(t *testing.T) {
	d := newTestDaemon(t)

	if got := d.state.ExistingIdentityForAccount("bbbbb"); got != nil {
		t.Errorf("an unknown account must have no identity, got %+v", got)
	}
	if _, minted := d.state.Accounts["bbbbb"]; minted {
		t.Fatal("the lookup itself must not create one")
	}

	// The minting variant is still the right tool for the enrolment path.
	created, err := d.state.IdentityForAccount("bbbbb")
	if err != nil || created == nil || created.PublicKey == "" {
		t.Fatalf("IdentityForAccount = %+v, %v; want a minted key for the enrolment path", created, err)
	}
	if got := d.state.ExistingIdentityForAccount("bbbbb"); got == nil || got.PublicKey != created.PublicKey {
		t.Error("once it exists, both must agree")
	}
}

// An empty account means "whatever is active", which is what the status and resume paths pass.
func TestExistingIdentityWithNoAccountMeansTheActiveOne(t *testing.T) {
	d := newTestDaemon(t)

	if got := d.state.ExistingIdentityForAccount(""); got != nil {
		t.Errorf("nothing is active yet, so there is nothing to return, got %+v", got)
	}

	if _, err := d.state.IdentityForAccount("aaaaa"); err != nil {
		t.Fatal(err)
	}
	d.state.ActiveAccountID = "aaaaa"

	if got := d.state.ExistingIdentityForAccount(""); got == nil || got.AccountID != "aaaaa" {
		t.Errorf("want the active account's identity, got %+v", got)
	}
}
