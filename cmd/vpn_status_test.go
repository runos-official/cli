package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/runos-official/cli/internal/vpn"
	"github.com/runos-official/cli/version"
	"github.com/spf13/cobra"
)

func TestVPNStatusShowsPrivateDNSFailure(t *testing.T) {
	status := &vpn.Status{Running: true, Interface: "runos0"}
	status.DNS = vpn.DNSStatus{
		Mode:  "unavailable",
		Error: "systemd-resolved is unavailable; private names can resolve publicly",
	}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatalf("print VPN status: %v", err)
	}
	if got := output.String(); !strings.Contains(got, "Private DNS: unavailable") || !strings.Contains(got, "resolve publicly") {
		t.Fatalf("status did not show the private DNS failure: %q", got)
	}
}

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

// A daemon older than the CLI is the commonest cause of a status field that reads empty, because
// the daemon serialises what ITS build knows and a field added later decodes as a zero value here.
// Measured 2026-08-19: daemon dev-2026-08-18T14:56:07Z against CLI dev-2026-08-18T19:40:41Z printed
// "Private DNS: unavailable" with no reason while split DNS was in fact working, with
// /etc/resolver written by the daemon and the cluster zone resolving to private addresses. A person
// reading that goes and debugs DNS. The status has both versions in hand and must say so.
func TestVPNStatusWarnsWhenTheDaemonIsAnOlderBuild(t *testing.T) {
	previous := version.Version
	version.Version = "dev-2026-08-18T19:40:41Z"
	defer func() { version.Version = previous }()

	status := &vpn.Status{Running: true, Interface: "utun8", Version: "dev-2026-08-18T14:56:07Z"}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatalf("print VPN status: %v", err)
	}
	got := output.String()
	for _, want := range []string{"older build", "dev-2026-08-18T14:56:07Z", "dev-2026-08-18T19:40:41Z", "runos vpn restart"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status did not name %q in the version mismatch: %q", want, got)
		}
	}
}

// The warning must NOT fire when the two agree, or every normal run carries noise.
func TestVPNStatusIsQuietWhenTheBuildsMatch(t *testing.T) {
	previous := version.Version
	version.Version = "dev-2026-08-18T19:40:41Z"
	defer func() { version.Version = previous }()

	status := &vpn.Status{Running: true, Interface: "utun8", Version: "dev-2026-08-18T19:40:41Z"}
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatalf("print VPN status: %v", err)
	}
	if strings.Contains(output.String(), "older build") {
		t.Fatalf("status warned about a version mismatch that does not exist: %q", output.String())
	}
}

/**
 * A tunnel that sends and never receives is not a pending state.
 *
 * MEASURED 2026-08-21: a Mac on the same LAN as its own cluster sat on "connected, no handshake
 * yet" indefinitely. Conductor had advertised the cluster's PUBLIC endpoint, the router does no NAT
 * hairpin, and tcpdump on the node captured nothing at all. Every field the status printed said
 * things were fine, so the line sent the reader nowhere.
 */
func TestClusterStatusLineDiagnosesOneWayTunnel(t *testing.T) {
	line := clusterStatusLine(vpn.ClusterStatus{
		CID: "v6b", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "169.1.210.215:32768",
		TxBytes:  296, RxBytes: 0,
	})

	if strings.Contains(line, "no handshake yet") {
		t.Fatalf("a one-way tunnel must not read as still-in-progress: %q", line)
	}
	// The endpoint has to be named. "No handshake" without it gives the reader nothing to test.
	if !strings.Contains(line, "169.1.210.215:32768") {
		t.Errorf("expected the endpoint in the line, got %q", line)
	}
	if !strings.Contains(line, "nothing back") {
		t.Errorf("expected the line to say nothing came back, got %q", line)
	}
	// A public endpoint is the hairpin case, and the way out is one command.
	if !strings.Contains(line, "NAT hairpin") || !strings.Contains(line, "ingress-not-published") {
		t.Errorf("expected the hairpin hint and the command that fixes it, got %q", line)
	}
}

// A private endpoint cannot be a hairpin problem, so that hint would be a wrong lead.
func TestClusterStatusLineOmitsHairpinHintForPrivateEndpoint(t *testing.T) {
	line := clusterStatusLine(vpn.ClusterStatus{
		CID: "v6b", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "192.168.0.132:32768",
		TxBytes:  296, RxBytes: 0,
	})
	if strings.Contains(line, "NAT hairpin") {
		t.Errorf("hairpin hint must not appear for a private endpoint: %q", line)
	}
	if !strings.Contains(line, "UDP reaches") {
		t.Errorf("expected the firewall/port-forward hint instead, got %q", line)
	}
}

// The genuine first-second case still reads as pending: nothing sent yet, nothing to diagnose.
func TestClusterStatusLineStillSaysPendingBeforeAnythingIsSent(t *testing.T) {
	line := clusterStatusLine(vpn.ClusterStatus{
		CID: "v6b", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "169.1.210.215:32768",
		TxBytes:  0, RxBytes: 0,
	})
	if !strings.Contains(line, "no handshake yet") {
		t.Errorf("expected the pending line before any traffic, got %q", line)
	}
}

// Traffic in BOTH directions with no recorded handshake is not the one-way case.
func TestClusterStatusLineDoesNotDiagnoseWhenBytesComeBack(t *testing.T) {
	line := clusterStatusLine(vpn.ClusterStatus{
		CID: "v6b", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "169.1.210.215:32768",
		TxBytes:  296, RxBytes: 92,
	})
	if !strings.Contains(line, "no handshake yet") {
		t.Errorf("expected the pending line when bytes come back, got %q", line)
	}
}

func TestIsPublicEndpoint(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		want     bool
	}{
		{"169.1.210.215:32768", true},
		{"192.168.0.132:32768", false},
		{"10.58.72.1:32768", false},
		{"172.16.4.4:32768", false},
		// Carrier-grade NAT: reached from inside the carrier's network, so not a hairpin case.
		{"100.72.3.4:32768", false},
		{"127.0.0.1:32768", false},
		// Unparseable or a host name: treated as public, because that is the case whose hint helps.
		{"vpn.example.com:32768", true},
		{"", true},
	} {
		if got := isPublicEndpoint(tc.endpoint); got != tc.want {
			t.Errorf("isPublicEndpoint(%q) = %v, want %v", tc.endpoint, got, tc.want)
		}
	}
}
