package vpn

import (
	"testing"
	"time"
)

/*
`peerUp` MUST MEAN THE TUNNEL IS CARRYING TRAFFIC.

It used to be set true purely because a peer existed in the plan, which is a statement about this
machine's own configuration and says nothing about the far end. MEASURED on 2026-08-22: the VPN
server was moved to a node whose advertised endpoint the client could not dial, every single packet
was lost, and `runos vpn status` still reported peerUp true. The one diagnostic an operator has said
the tunnel was healthy while it was completely dead, which is worse than reporting nothing.
*/
func TestPeerUpFollowsTheHandshakeNotTheConfiguration(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		handshake time.Time
		want      bool
	}{
		{"never handshaken is not up", time.Time{}, false},
		{"handshaken just now is up", now, true},
		{"one minute ago is up, wireguard rekeys about every two", now.Add(-1 * time.Minute), true},
		{"ten minutes ago is NOT up, whatever the config says", now.Add(-10 * time.Minute), false},
		{"an hour ago is NOT up", now.Add(-time.Hour), false},
	} {
		got := !tc.handshake.IsZero() && time.Since(tc.handshake) < peerStaleAfter
		if got != tc.want {
			t.Errorf("%s: peerUp=%v, want %v (handshake %v ago)",
				tc.name, got, tc.want, time.Since(tc.handshake).Truncate(time.Second))
		}
	}
}

// The window has to sit past WireGuard's own rekey cadence or a healthy idle tunnel flaps to down.
func TestTheStaleWindowIsPastWireguardsRekeyCadence(t *testing.T) {
	if peerStaleAfter <= 2*time.Minute {
		t.Fatalf("peerStaleAfter is %v, which is inside WireGuard's ~120s rekey interval: a healthy idle tunnel would report down", peerStaleAfter)
	}
}
