package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/vpn"
)

/*
Updating with a tunnel up must say, in plain words, what will happen to the tunnel.

THE GAP. A person updated with the VPN connected and lost their connection. Nothing in `runos update`
mentioned the tunnel before it replaced the program, and the notice after it named a command and a
password but not the consequence: the restart cuts the tunnel for a few seconds, and the service
picks the session up again by itself. Without that sentence the person cannot tell an expected gap
from a fault, and reaches for the one switch they know.
*/

func TestTheNoticeBeforeAnUpdateIsSilentWhenNoTunnelIsUp(t *testing.T) {
	if got := vpnTunnelUpdateNotice(false); got != "" {
		t.Errorf("a machine with no tunnel must read nothing, got %q", got)
	}
}

func TestTheNoticeBeforeAnUpdateSaysTheTunnelStaysUp(t *testing.T) {
	notice := vpnTunnelUpdateNotice(true)
	for _, want := range []string{
		"VPN is connected",
		"does not change",       // the update itself leaves the tunnel alone
		"few seconds",           // the later restart is short
		"without a new sign-in", // and needs no sign-in
		"sudo runos vpn restart",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice must contain %q, got %q", want, notice)
		}
	}
}

func TestTheRestartNoticeSaysWhatARestartDoesToALiveTunnel(t *testing.T) {
	notice := vpnRestartNotice("1.17.0", "1.18.0", tunnelLive)
	for _, want := range []string{"few seconds", "without a new sign-in"} {
		if !strings.Contains(notice, want) {
			t.Errorf("restart notice must contain %q, got %q", want, notice)
		}
	}
}

// With no tunnel there is nothing to reconnect, so the notice must not promise it.
func TestTheRestartNoticeMakesNoPromiseWhenNoTunnelIsUp(t *testing.T) {
	notice := vpnRestartNotice("1.17.0", "1.18.0", tunnelDown)
	for _, banned := range []string{"few seconds", "connects again", "reconnect"} {
		if strings.Contains(notice, banned) {
			t.Errorf("restart notice must not contain %q when no tunnel is up, got %q", banned, notice)
		}
	}
	if !strings.Contains(notice, "sudo runos vpn restart") {
		t.Errorf("must still name the command, got %q", notice)
	}
}

// A tunnel whose session has ended does not come back by itself: the person signs in again.
func TestTheRestartNoticeSaysSignInAgainWhenTheSessionHasEnded(t *testing.T) {
	notice := vpnRestartNotice("1.17.0", "1.18.0", tunnelLapsed)
	if strings.Contains(notice, "connects again by itself") {
		t.Errorf("must not promise a reconnect for an ended session, got %q", notice)
	}
	if !strings.Contains(notice, "sign in again") {
		t.Errorf("must say to sign in again, got %q", notice)
	}
}

func TestTunnelStateComesFromTheDaemonStatus(t *testing.T) {
	live := &vpn.Status{Running: true}
	live.Session.Present = true
	lapsed := &vpn.Status{Running: true}
	lapsed.Session.Present = true
	lapsed.Session.LoginRequired = true
	expired := &vpn.Status{Running: true} // session not present
	for _, tc := range []struct {
		name   string
		status *vpn.Status
		want   tunnelState
	}{
		{"no answer", nil, tunnelDown},
		{"not running", &vpn.Status{}, tunnelDown},
		{"running with a live session", live, tunnelLive},
		{"running and login required", lapsed, tunnelLapsed},
		{"running without a session", expired, tunnelLapsed},
	} {
		if got := tunnelStateOf(tc.status); got != tc.want {
			t.Errorf("%s: tunnelStateOf = %v, want %v", tc.name, got, tc.want)
		}
	}
}
