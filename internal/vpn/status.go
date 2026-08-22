package vpn

import "time"

/*
peerStaleAfter is how long since the last WireGuard handshake before a peer is reported DOWN.

WireGuard renews a handshake roughly every 120 seconds while traffic flows, and gives up on a
rekey attempt after 90. Three minutes is therefore comfortably past "a healthy tunnel would have
handshaken by now" without flapping the report on a link that is merely idle.
*/
const peerStaleAfter = 3 * time.Minute

// statusLocked renders the daemon's current view for `runos vpn status`. Split from daemon.go to
// keep each file inside the size budget; the daemon holds the lock, this only reads.
func (d *Daemon) statusLocked() *Status {
	s := &Status{SchemaVersion: 2, Version: d.version, DNS: d.dns}
	active := d.state.Active()
	if active != nil {
		s.DeviceID = active.DeviceID
		s.AccountID = active.AccountID
	}
	s.Running = d.engine != nil
	if d.engine != nil {
		s.Interface = d.engine.InterfaceName()
	}
	if d.plan.Address.IsValid() {
		s.Address = d.plan.Address.String()
	}
	if active != nil {
		s.Session.Present = active.SessionToken != "" && active.SessionExpiresAt.After(time.Now())
		s.Session.ExpiresAt = active.SessionExpiresAt
		s.Session.LoginRequired = d.plan.LoginRequired || (active.SessionToken != "" && !s.Session.Present)
	}
	s.Revision = d.revision
	s.LastPollAt = d.lastPoll
	s.LastPollErr = d.lastPollErr
	s.LastApplyErr = d.lastApplyErr

	var stats map[string]*PeerStats
	if d.engine != nil {
		stats, _ = d.engine.Stats()
	}
	peerByCID := map[string]PeerPlan{}
	for _, p := range d.plan.Peers {
		peerByCID[p.CID] = p
	}
	if d.doc != nil {
		for _, c := range d.doc.Clusters {
			cs := ClusterStatus{
				CID: c.CID, Name: c.Name, Connected: c.Connected,
				Reachable: c.Server.Reachable, Reason: c.Server.Reason,
				Endpoint: c.Server.Endpoint, AllowedIPs: c.AllowedIPs,
				Resolver: c.DNS.Resolver, Zones: c.DNS.Zones, PeeredWith: c.PeeredWith,
			}
			if peer, ok := peerByCID[c.CID]; ok {
				if stats != nil {
					if st := stats[peer.PublicKeyHex]; st != nil {
						cs.LastHandshake = st.LastHandshake
						cs.RxBytes = st.RxBytes
						cs.TxBytes = st.TxBytes
					}
				}
				/*
				 * PEER UP MEANS THE TUNNEL IS CARRYING TRAFFIC, not that a peer is configured.
				 *
				 * It used to be set to true purely because a peer existed in the plan, which is a
				 * statement about this machine's own configuration and says nothing about the far
				 * end. MEASURED 2026-08-22: the VPN server was moved to a node whose advertised
				 * endpoint the client could not dial, every packet was lost, and `runos vpn status`
				 * still reported peerUp true and reachable true. The one diagnostic an operator has
				 * said the tunnel was fine while it was completely dead.
				 *
				 * WireGuard rekeys about every two minutes, so a handshake older than three is a
				 * tunnel that is not passing traffic. A peer that has NEVER handshaken has a zero
				 * time and is not up either.
				 */
				cs.PeerUp = !cs.LastHandshake.IsZero() && time.Since(cs.LastHandshake) < peerStaleAfter
			}
			s.Clusters = append(s.Clusters, cs)
		}
	}
	return s
}
