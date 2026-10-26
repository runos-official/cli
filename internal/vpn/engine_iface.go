package vpn

// tunnelEngine is what the daemon needs from the WireGuard layer. The real one is *engine; the
// interface exists so a test can bring a tunnel "up" without creating a network interface, which
// needs root.
type tunnelEngine interface {
	InterfaceName() string
	Up() error
	ApplyPlan(privateKeyHex string, plan Plan) error
	Stats() (map[string]*PeerStats, error)
	Close()
}

// createEngine makes the tun interface and the WireGuard device. A variable so a test can prove WHEN
// the daemon asks for a tunnel, and hand back a fake one.
var createEngine = func(requestedName string, verbose bool) (tunnelEngine, error) {
	eng, err := newEngine(requestedName, verbose)
	if err != nil {
		return nil, err
	}
	return eng, nil
}
