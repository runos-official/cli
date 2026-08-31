package cmd

import (
	"strings"
	"testing"
)

/*
What an account switch does to a tunnel that belongs to the previous identity.

FPL26 D3: the tunnel never outlives the identity that opened it. Switching account drops it and
leaves it down until the person asks for it back.

WHAT IT USED TO DO. `synchronizeVPNAccount` enrolled this machine under the new account, minted a
session and called `up`, unconditionally. Two things were wrong with that.

It CONNECTED A VPN NOBODY ASKED FOR. The old code never looked at whether the tunnel was running,
so switching account on a machine with the VPN deliberately off turned it on. That exact complaint
was already reported against RunOS Desktop, whose automatic account-follow did the same thing:
"even though i don't have the connect at startup option selected, i seem to be connected". The app's
copy was removed. This one was not.

And it was A SECOND COPY of the enrol-mint-up sequence, beside the one in `vpn up`. Two copies of an
account-scoped sequence is exactly the thing that drifts and then differs in the case nobody tested,
which is how the account-switch defects got in.

Dropping the tunnel needs none of it.
*/

func TestSwitchingAccountLeavesTheVPNDownRatherThanConnectingIt(t *testing.T) {
	daemon := newFakeDaemon(t)

	result := disconnectVPNForAccountChange(daemon.path, "aaaaa", "bbbbb")

	if result.Synchronized {
		t.Error("an account switch must not connect anything; the person asks for that themselves")
	}
	if result.State != vpnStateDisconnected {
		t.Errorf("state = %q, want %q", result.State, vpnStateDisconnected)
	}
	// THE DEFECT ITSELF: no enrolment, no mint, no up. Only a teardown.
	for _, call := range daemon.recorded() {
		if strings.HasPrefix(call, "up ") {
			t.Errorf("an account switch must never bring a tunnel up, got %q", call)
		}
	}
	assertHasCall(t, daemon.recorded(), "down")
}

// The person has to be told the tunnel went, and how to get it back on the account they just
// switched to. Silence here reads as the VPN having broken.
func TestTheSwitchSaysTheVPNWentAndHowToGetItBack(t *testing.T) {
	daemon := newFakeDaemon(t)

	message := disconnectVPNForAccountChange(daemon.path, "aaaaa", "bbbbb").Message

	if !strings.Contains(message, "runos vpn up") {
		t.Errorf("must name the way back, got %q", message)
	}
	if !strings.Contains(strings.ToLower(message), "disconnect") {
		t.Errorf("must say the tunnel went, got %q", message)
	}
}

/*
Switching to the account you are already on is not a change, so nothing is torn down.

`runos account switch <same-id>` is a re-authentication, which people do to refresh a sign-in. It
would be an unpleasant surprise if it dropped their tunnel.
*/
func TestReAuthenticatingTheSameAccountLeavesTheTunnelAlone(t *testing.T) {
	daemon := newFakeDaemon(t)

	result := disconnectVPNForAccountChange(daemon.path, "aaaaa", "aaaaa")

	if result.State != vpnStateUnchanged {
		t.Errorf("state = %q, want %q", result.State, vpnStateUnchanged)
	}
	if calls := daemon.recorded(); len(calls) != 0 {
		t.Errorf("the daemon must not be touched at all, got %v", calls)
	}
}

/*
A machine with no VPN service still switches accounts.

Most machines have no root daemon: `runos desktop install` does not write one. A socket that is not
there is the ordinary case, not a failure, and it must never stop somebody changing account.
*/
func TestSwitchingAccountWorksWithNoVPNService(t *testing.T) {
	result := disconnectVPNForAccountChange("/nonexistent/no-daemon.sock", "aaaaa", "bbbbb")

	if result.State != vpnStateNotRunning {
		t.Errorf("state = %q, want %q", result.State, vpnStateNotRunning)
	}
	if strings.Contains(strings.ToLower(result.Message), "error") {
		t.Errorf("an absent daemon is not an error to report, got %q", result.Message)
	}
}
