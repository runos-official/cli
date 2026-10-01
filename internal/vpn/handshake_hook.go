package vpn

import "time"

// watchHandshakesLocked reads each peer's last handshake from the engine and logs what changed.
// Called on every poll, success or not, because a control-plane outage does not stop the tunnel and
// a handshake can still be observed through one. Holds d.mu (the caller does).
func (d *Daemon) watchHandshakesLocked() {
	if d.engine == nil {
		d.handshakes = nil
		return
	}
	if d.handshakes == nil {
		d.handshakes = newHandshakeWatch()
	}
	stats, err := d.engine.Stats()
	if err != nil {
		return
	}
	obs := make([]peerObs, 0, len(d.appliedPeers))
	for _, p := range d.appliedPeers {
		o := peerObs{CID: p.CID, Endpoint: p.Endpoint}
		if st := stats[p.PublicKeyHex]; st != nil {
			o.LastHandshake = st.LastHandshake
		}
		obs = append(obs, o)
	}
	for _, line := range d.handshakes.observe(time.Now(), obs) {
		logStepLine(line)
	}
}
