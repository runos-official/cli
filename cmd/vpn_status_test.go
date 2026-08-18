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
			// The defect the operator reported: a cluster with no VPN server was described as
			// "available but no VPN server", which reads as an offer. It is not one. `vpn connect`
			// on it produces a connected-but-unreachable dead state and nothing else. A person
			// scanning this list must be able to see, in the first word, which clusters they can
			// actually use.
			name:    "no server is NOT available",
			cluster: vpn.ClusterStatus{CID: "jwn", Name: "y", Connected: false, Reachable: false, Reason: "no VPN server installed"},
			want:    "cannot connect: no VPN server installed",
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

// "available" is an offer, and the list must only make it to clusters that can actually be
// connected. This is the whole of the operator's report: "i am connecting to nothing it seems yet
// it says i can connect".
func TestOnlyAConnectableClusterIsCalledAvailable(t *testing.T) {
	noServer := vpn.ClusterStatus{CID: "jwn", Name: "y", Connected: false, Reachable: false, Reason: "no VPN server installed"}
	if got := clusterStatusLine(noServer); strings.Contains(got, "available") {
		t.Errorf("a cluster with no VPN server must not be offered as available, got %q", got)
	}
	ready := vpn.ClusterStatus{CID: "z42", Name: "x", Connected: false, Reachable: true}
	if got := clusterStatusLine(ready); !strings.Contains(got, "available") {
		t.Errorf("a cluster with a server IS available, got %q", got)
	}
}

// A cluster already stuck in the dead state must say how to get out of it, because the state is
// not one the person can fix by waiting.
func TestConnectedButUnreachableSaysWhatToDo(t *testing.T) {
	stuck := vpn.ClusterStatus{CID: "g4v", Name: "vhm-lab", Connected: true, Reachable: false, Reason: "no VPN server installed"}
	got := clusterStatusLine(stuck)
	if !strings.Contains(got, "no VPN server installed") {
		t.Errorf("must keep the reason, got %q", got)
	}
	if !strings.Contains(got, "disconnect g4v") {
		t.Errorf("must name the way out, got %q", got)
	}
}
