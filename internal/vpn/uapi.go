package vpn

import (
	"bufio"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// The wireguard-go UAPI is the text `wg(8)` speaks over the control socket. The daemon renders a
// Plan into one `set` transaction and parses one `get` dump into peer statistics. Both are pure
// so the engine wrapper stays a thin adapter.

// RenderUAPISet renders the full configuration for a Plan: the private key, then EVERY peer with
// `replace_peers=true` so peers not in the plan disappear. Sending the whole set every time is
// what makes the engine converge rather than accumulate, the same rule as the wg1 servers.
func RenderUAPISet(privateKeyHex string, plan Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privateKeyHex)
	b.WriteString("replace_peers=true\n")
	for _, peer := range plan.Peers {
		fmt.Fprintf(&b, "public_key=%s\n", peer.PublicKeyHex)
		fmt.Fprintf(&b, "endpoint=%s\n", peer.Endpoint)
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", peer.Keepalive)
		b.WriteString("replace_allowed_ips=true\n")
		for _, prefix := range peer.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", prefix.String())
		}
	}
	return b.String()
}

// PeerStats is what a `get` dump says about one peer.
type PeerStats struct {
	PublicKeyHex  string
	Endpoint      string
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
	AllowedIPs    []netip.Prefix
}

// ParseUAPIGet reads a `get` dump into per-peer statistics keyed by public key (hex).
func ParseUAPIGet(dump string) (map[string]*PeerStats, error) {
	stats := map[string]*PeerStats{}
	var current *PeerStats
	scanner := bufio.NewScanner(strings.NewReader(dump))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("uapi line %q has no '='", line)
		}
		switch key {
		case "public_key":
			current = &PeerStats{PublicKeyHex: value}
			stats[value] = current
		case "endpoint":
			if current != nil {
				current.Endpoint = value
			}
		case "last_handshake_time_sec":
			if current != nil {
				secs, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("uapi %s=%q: %w", key, value, err)
				}
				if secs > 0 {
					current.LastHandshake = time.Unix(secs, 0)
				}
			}
		case "rx_bytes":
			if current != nil {
				current.RxBytes, _ = strconv.ParseInt(value, 10, 64)
			}
		case "tx_bytes":
			if current != nil {
				current.TxBytes, _ = strconv.ParseInt(value, 10, 64)
			}
		case "allowed_ip":
			if current != nil {
				if prefix, err := netip.ParsePrefix(value); err == nil {
					current.AllowedIPs = append(current.AllowedIPs, prefix)
				}
			}
		}
	}
	return stats, scanner.Err()
}
