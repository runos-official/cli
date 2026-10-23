package cmd

import (
	"strings"
	"testing"
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

func TestTheRestartNoticeSaysWhatARestartDoesToTheTunnel(t *testing.T) {
	notice := vpnRestartNotice("1.17.0", "1.18.0")
	for _, want := range []string{"few seconds", "without a new sign-in"} {
		if !strings.Contains(notice, want) {
			t.Errorf("restart notice must contain %q, got %q", want, notice)
		}
	}
}
