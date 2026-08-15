package vpn

import (
	"net/netip"
	"strings"
	"testing"
)

// The UAPI transaction is what the engine actually receives, so its shape is the requirement:
// the whole peer set every time (replace_peers), each peer pinned to its allowed IPs
// (replace_allowed_ips), keepalive on, and the key in hex.
func TestRenderUAPISetSendsTheWholeSetWithReplaceSemantics(t *testing.T) {
	plan := Plan{Peers: []PeerPlan{{
		CID: "aaa", PublicKeyHex: strings.Repeat("ab", 32), Endpoint: "203.0.113.5:32768", Keepalive: 25,
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.10.10.0/24")},
	}}}
	got := RenderUAPISet(strings.Repeat("cd", 32), plan)
	want := "private_key=" + strings.Repeat("cd", 32) + "\nreplace_peers=true\n" +
		"public_key=" + strings.Repeat("ab", 32) + "\nendpoint=203.0.113.5:32768\npersistent_keepalive_interval=25\nreplace_allowed_ips=true\nallowed_ip=10.10.10.0/24\n"
	if got != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
	// An empty plan still says replace_peers, which is how "remove them all" is expressed.
	if empty := RenderUAPISet("00", Plan{}); !strings.Contains(empty, "replace_peers=true") {
		t.Errorf("empty plan must clear the peers, got %q", empty)
	}
}

func TestParseUAPIGetReadsPerPeerHandshakeAndTraffic(t *testing.T) {
	dump := "private_key=" + strings.Repeat("cd", 32) + "\nlisten_port=51820\n" +
		"public_key=" + strings.Repeat("ab", 32) + "\nendpoint=203.0.113.5:32768\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=5\ntx_bytes=1500\nrx_bytes=2500\npersistent_keepalive_interval=25\nallowed_ip=10.10.10.0/24\n" +
		"public_key=" + strings.Repeat("ef", 32) + "\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\ntx_bytes=0\nrx_bytes=0\n"
	stats, err := ParseUAPIGet(dump)
	if err != nil {
		t.Fatal(err)
	}
	a := stats[strings.Repeat("ab", 32)]
	if a == nil || a.Endpoint != "203.0.113.5:32768" || a.TxBytes != 1500 || a.RxBytes != 2500 || a.LastHandshake.Unix() != 1700000000 {
		t.Errorf("peer a = %+v", a)
	}
	if len(a.AllowedIPs) != 1 || a.AllowedIPs[0].String() != "10.10.10.0/24" {
		t.Errorf("peer a allowed ips = %v", a.AllowedIPs)
	}
	// A peer that never handshook has a zero time, not 1970.
	if b := stats[strings.Repeat("ef", 32)]; b == nil || !b.LastHandshake.IsZero() {
		t.Errorf("peer b = %+v, want zero handshake", b)
	}
}
