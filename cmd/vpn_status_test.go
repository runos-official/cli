package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/vpn"
)

// The status line is what a person reads to know whether their tunnel is working, so its wording
// is the requirement: a connected-and-handshaking cluster shows traffic, a connected-but-
// unreachable one shows the reason, an available one says it is not connected.
func TestClusterStatusLineReflectsEachState(t *testing.T) {
	cases := []struct {
		name    string
		cluster vpn.ClusterStatus
		want    string
	}{
		{
			name:    "connected and handshaking",
			cluster: vpn.ClusterStatus{CID: "g4v", Name: "lab", Connected: true, Reachable: true, PeerUp: true, LastHandshake: time.Now().Add(-30 * time.Second), RxBytes: 2048, TxBytes: 1024},
			want:    "connected, last handshake",
		},
		{
			name:    "connected but no server",
			cluster: vpn.ClusterStatus{CID: "wm2", Name: "hz", Connected: true, Reachable: false, Reason: "no VPN server installed"},
			want:    "connected but unreachable: no VPN server installed",
		},
		{
			name:    "available not connected",
			cluster: vpn.ClusterStatus{CID: "z42", Name: "x", Connected: false, Reachable: true},
			want:    "available (not connected)",
		},
		{
			name:    "available no server",
			cluster: vpn.ClusterStatus{CID: "jwn", Name: "y", Connected: false, Reachable: false, Reason: "no VPN server installed"},
			want:    "available but no VPN server",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterStatusLine(tc.cluster); !strings.Contains(got, tc.want) {
				t.Errorf("line = %q, want to contain %q", got, tc.want)
			}
		})
	}
}

// A peering never routes the peer's addresses (peering is not transit); status must say so, and
// only when a connected cluster is peered with one the device is NOT connected to.
func TestPeeringNoteNamesUnroutedPeers(t *testing.T) {
	both := []vpn.ClusterStatus{
		{CID: "wm2", Connected: true, PeeredWith: []string{"g4v"}},
		{CID: "g4v", Connected: false, PeeredWith: []string{"wm2"}},
	}
	note := peeringNote(both)
	if !strings.Contains(note, "wm2 is peered with g4v") || !strings.Contains(note, "unrouted") || !strings.Contains(note, "connect g4v") {
		t.Fatalf("note = %q", note)
	}
	both[1].Connected = true
	if got := peeringNote(both); got != "" {
		t.Fatalf("both connected must be silent, got %q", got)
	}
	if got := peeringNote([]vpn.ClusterStatus{{CID: "a"}, {CID: "b", Connected: true}}); got != "" {
		t.Fatalf("no peering must be silent, got %q", got)
	}
}

func TestBytesHumanUsesBinaryUnits(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1048576: "1.0 MiB"}
	for n, want := range cases {
		if got := bytesHuman(n); got != want {
			t.Errorf("bytesHuman(%d) = %q, want %q", n, got, want)
		}
	}
}
