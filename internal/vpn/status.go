package vpn

import "time"

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
				cs.PeerUp = true
				if stats != nil {
					if st := stats[peer.PublicKeyHex]; st != nil {
						cs.LastHandshake = st.LastHandshake
						cs.RxBytes = st.RxBytes
						cs.TxBytes = st.TxBytes
					}
				}
			}
			s.Clusters = append(s.Clusters, cs)
		}
	}
	return s
}
