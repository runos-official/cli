package vpn

import (
	"fmt"
	"net/netip"
)

// convergeRoutesAndDNSLocked brings the routes and the split DNS to the plan. haveRoutes is what
// the daemon believes is on the machine: the previous plan's routes when a document changed
// (so dropped clusters lose their route), nil to re-assert every route (AddRoute is idempotent
// on every platform). Resolvers are always read back from the platform.
func (d *Daemon) convergeRoutesAndDNSLocked(haveRoutes []netip.Prefix, plan Plan) error {
	if d.engine == nil {
		return nil
	}
	iface := d.engine.InterfaceName()
	if err := d.convergeRoutesLocked(iface, haveRoutes, plan); err != nil {
		stepOutcome("routes", err, "")
		return err
	}
	stepOutcome("routes", nil, fmt.Sprintf("%d route(s) via %s", len(plan.Routes), iface))
	if d.closed.Load() {
		return errStopping
	}

	clientAddr := netip.Addr{}
	if plan.Address.IsValid() {
		clientAddr = plan.Address.Addr()
	}
	dns, err := d.platform.ReconcileDNS(iface, clientAddr, plan.Resolvers)
	// A zone macOS has not listed yet is not a failure: the tunnel, the address and the routes are
	// up, and a background watch finishes the check (dns_pending.go).
	return d.applyDNSResultLocked(dns, err, plan.Resolvers)
}

// convergeRoutesLocked adds the routes the plan has and the machine lacks, and removes the reverse.
func (d *Daemon) convergeRoutesLocked(iface string, haveRoutes []netip.Prefix, plan Plan) error {
	routeDiff := DiffPrefixes(haveRoutes, plan.Routes)
	for _, prefix := range routeDiff.Add {
		if d.closed.Load() {
			return errStopping
		}
		if err := d.platform.AddRoute(iface, prefix); err != nil {
			return err
		}
	}
	for _, prefix := range routeDiff.Remove {
		if err := d.platform.RemoveRoute(iface, prefix); err != nil {
			return err
		}
	}
	return nil
}
