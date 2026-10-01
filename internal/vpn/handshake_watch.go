package vpn

import (
	"fmt"
	"time"
)

/*
The first handshake is the step that proves a tunnel, and the daemon never performs it: wireguard-go
does, on its own schedule, after the peers are loaded. So the daemon READS it back on each poll and
writes a line only when something changes.

  - first handshake with a peer: one ok line.
  - no handshake after a grace period: one failed line, naming the usual cause.
  - a working peer going quiet for longer than a healthy tunnel does: one warn line, and one ok line
    when it resumes.

A tunnel that works writes nothing after its first line, which is what keeps this inside the rule
against a per-poll heartbeat (daemonlog.go).
*/

// handshakeGrace is how long a freshly configured peer may go without a handshake before that is
// reported. WireGuard retries every 5 seconds, so a minute is a dozen failed attempts.
const handshakeGrace = time.Minute

// peerObs is what one poll observed about one peer.
type peerObs struct {
	CID           string
	Endpoint      string
	LastHandshake time.Time // zero: never
}

type peerWatch struct {
	since       time.Time
	established bool
	stale       bool
	warned      bool
}

type handshakeWatch struct {
	peers map[string]*peerWatch
}

func newHandshakeWatch() *handshakeWatch {
	return &handshakeWatch{peers: map[string]*peerWatch{}}
}

// observe takes this poll's view of every peer and returns the step lines for what changed.
func (w *handshakeWatch) observe(now time.Time, peers []peerObs) []string {
	var lines []string
	seen := map[string]bool{}
	for _, p := range peers {
		seen[p.CID] = true
		s := w.peers[p.CID]
		if s == nil {
			s = &peerWatch{since: now}
			w.peers[p.CID] = s
		}
		fresh := !p.LastHandshake.IsZero() && now.Sub(p.LastHandshake) < peerStaleAfter
		switch {
		case fresh && !s.established:
			s.established, s.stale = true, false
			lines = append(lines, stepBody("handshake", statusOK, fmt.Sprintf(
				"first handshake with cluster %s after %s", p.CID, now.Sub(s.since).Round(time.Second))))
		case fresh && s.stale:
			s.stale = false
			lines = append(lines, stepBody("handshake", statusOK,
				fmt.Sprintf("handshake with cluster %s resumed", p.CID)))
		case !fresh && s.established && !s.stale:
			s.stale = true
			lines = append(lines, stepBody("handshake", statusWarn, fmt.Sprintf(
				"no handshake with cluster %s for %s: the tunnel is not carrying traffic",
				p.CID, now.Sub(p.LastHandshake).Round(time.Second))))
		case !fresh && !s.established && !s.warned && now.Sub(s.since) >= handshakeGrace:
			s.warned = true
			lines = append(lines, stepBody("handshake", statusFailed, fmt.Sprintf(
				"no handshake with cluster %s after %s: the server at %s did not answer. Usual causes: "+
					"UDP to that endpoint is blocked by this network or a firewall, the endpoint is wrong, "+
					"or the server rejected this device's key",
				p.CID, now.Sub(s.since).Round(time.Second), p.Endpoint)))
		}
	}
	for cid := range w.peers {
		if !seen[cid] {
			delete(w.peers, cid)
		}
	}
	return lines
}
