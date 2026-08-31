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
			cluster: vpn.ClusterStatus{CID: "cl3", Name: "lab", Connected: true, Reachable: true, PeerUp: true, LastHandshake: time.Now().Add(-30 * time.Second), RxBytes: 2048, TxBytes: 1024},
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
			cluster: vpn.ClusterStatus{CID: "cl4", Name: "y", Connected: false, Reachable: false, Reason: "no VPN server installed"},
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
		{CID: "wm2", Connected: true, PeeredWith: []string{"cl3"}},
		{CID: "cl3", Connected: false, PeeredWith: []string{"wm2"}},
	}
	note := peeringNote(both)
	if !strings.Contains(note, "wm2 is peered with cl3") || !strings.Contains(note, "unrouted") || !strings.Contains(note, "connect cl3") {
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
	noServer := vpn.ClusterStatus{CID: "cl4", Name: "y", Connected: false, Reachable: false, Reason: "no VPN server installed"}
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
	stuck := vpn.ClusterStatus{CID: "cl3", Name: "vhm-lab", Connected: true, Reachable: false, Reason: "no VPN server installed"}
	got := clusterStatusLine(stuck)
	if !strings.Contains(got, "no VPN server installed") {
		t.Errorf("must keep the reason, got %q", got)
	}
	if !strings.Contains(got, "disconnect cl3") {
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
		CID: "cl1", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "203.0.113.15:32768",
		TxBytes:  296, RxBytes: 0,
	})

	if strings.Contains(line, "no handshake yet") {
		t.Fatalf("a one-way tunnel must not read as still-in-progress: %q", line)
	}
	// The endpoint has to be named. "No handshake" without it gives the reader nothing to test.
	if !strings.Contains(line, "203.0.113.15:32768") {
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
		CID: "cl1", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "192.168.7.20:32768",
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
		CID: "cl1", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "203.0.113.15:32768",
		TxBytes:  0, RxBytes: 0,
	})
	if !strings.Contains(line, "no handshake yet") {
		t.Errorf("expected the pending line before any traffic, got %q", line)
	}
}

// Traffic in BOTH directions with no recorded handshake is not the one-way case.
func TestClusterStatusLineDoesNotDiagnoseWhenBytesComeBack(t *testing.T) {
	line := clusterStatusLine(vpn.ClusterStatus{
		CID: "cl1", Name: "home-2node",
		Connected: true, Reachable: true, PeerUp: true,
		Endpoint: "203.0.113.15:32768",
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
		{"203.0.113.15:32768", true},
		{"192.168.7.20:32768", false},
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

// MEASURED 2026-08-22 on the lab boxes, and it produced a FALSE defect before it was understood.
// The VPN session expired at 19:54. `runos vpn status` still led with:
//
//	VPN: up on utun0 (198.51.100.3/32)
//	Session: expired - run 'runos vpn up' to sign in again
//
// The interface IS up, so the first line is literally true, but every cluster route had been
// withdrawn: `netstat -rn | grep utun0` showed ONLY the client's own /32, no overlay or VM pool
// range. Traffic to a VM left by the LAN default gateway and vanished. A live-migration
// measurement running across that boundary looked like the VM was unreachable for 300+ seconds,
// and that was read as a migration defect until the expiry was spotted.
//
// A reader scanning the first line must not come away thinking the tunnel works.
func TestVPNStatusHeadlineSaysExpiredRatherThanUp(t *testing.T) {
	status := &vpn.Status{Running: true, Interface: "utun0", Address: "198.51.100.3/32"}
	status.Session.LoginRequired = true

	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatalf("print VPN status: %v", err)
	}

	got := output.String()
	headline := strings.SplitN(got, "\n", 2)[0]
	if !strings.Contains(strings.ToUpper(headline), "EXPIRED") {
		t.Fatalf("headline must say the session is expired, got %q", headline)
	}
	if strings.HasPrefix(headline, "VPN: up on") {
		t.Fatalf("headline still reads as a working tunnel: %q", headline)
	}
}

// A live session must keep the plain, reassuring headline.
func TestVPNStatusHeadlineStaysUpWhenTheSessionIsLive(t *testing.T) {
	status := &vpn.Status{Running: true, Interface: "utun0", Address: "198.51.100.3/32"}
	status.Session.Present = true
	status.Session.ExpiresAt = time.Now().Add(2 * time.Hour)

	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	if err := printVPNStatus(command, status); err != nil {
		t.Fatalf("print VPN status: %v", err)
	}
	headline := strings.SplitN(output.String(), "\n", 2)[0]
	if headline != "VPN: up on utun0 (198.51.100.3/32)" {
		t.Fatalf("a live session should read plainly, got %q", headline)
	}
}

// The same 2026-08-22 expiry. Under the headline every cluster row read "connecting...", which is
// the exact failure this file already warns about elsewhere: a state that "reads as 'still working
// on it' forever". Nothing is connecting, because the session that authorises the routes is gone.
func TestClusterRowsSayExpiredRatherThanConnecting(t *testing.T) {
	c := vpn.ClusterStatus{CID: "cl1", Name: "host-homelab", Connected: true, Reachable: true}

	if got := clusterStatusLineWithSession(c, true); !strings.Contains(got, "session expired") {
		t.Fatalf("an expired session must be named on the row, got %q", got)
	}
	if got := clusterStatusLineWithSession(c, true); strings.Contains(got, "connecting...") {
		t.Fatalf("row still implies progress that cannot happen: %q", got)
	}
	// With a live session the row is unchanged.
	if got := clusterStatusLineWithSession(c, false); !strings.Contains(got, "connecting...") {
		t.Fatalf("a live session should keep the normal row, got %q", got)
	}
}

/*
What the VPN status tells a SIGNED-OUT person to do.

MEASURED 2026-08-31 on a live machine, right after `runos logout`:

	$ runos vpn status
	VPN: down
	Session: none - run 'runos vpn up'

	$ runos vpn up
	Error: you are not signed in. Run 'runos login' first, then 'runos vpn up'

The status pointed at a command that immediately refuses. Connecting consumes a sign-in and never
creates one (FPL26 D1), so with no identity the next step is `runos login`, not `runos vpn up`.
*/
func TestTheSessionLineNamesTheStepThatCanActuallyWork(t *testing.T) {
	if got := sessionNextStep(false); !strings.Contains(got, "runos login") {
		t.Errorf("a signed-out person must be sent to sign in, got %q", got)
	}
	/*
	 Naming BOTH steps is right; naming them in the wrong order is not. `vpn up` is where they are
	 going, and it refuses until the sign-in happens, so the sign-in has to come first in the
	 sentence as well as in time. This is the same two-step phrasing `vpn up` itself uses when it
	 refuses, so a person meets one wording rather than two.
	*/
	got := sessionNextStep(false)
	login, up := strings.Index(got, "runos login"), strings.Index(got, "runos vpn up")
	if up >= 0 && login > up {
		t.Errorf("the sign-in must come first, got %q", got)
	}
	// Signed in with no session is the ordinary "not connected yet" state, and `up` is right there.
	if got := sessionNextStep(true); !strings.Contains(got, "runos vpn up") {
		t.Errorf("a signed-in person is one command from a tunnel, got %q", got)
	}
}
