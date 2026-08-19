package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/runos-official/cli/internal/vpn"
	"github.com/runos-official/cli/version"

	"github.com/spf13/cobra"
)

// printVPNStatus renders the human view of a daemon status: the session state first (it is what
// lapses), then one line per cluster with its connection, reachability and live traffic.
func printVPNStatus(cmd *cobra.Command, status *vpn.Status) error {
	out := cmd.OutOrStdout()
	if status == nil {
		fmt.Fprintln(out, "The VPN is not running.")
		return nil
	}

	if !status.Running {
		fmt.Fprintln(out, "VPN: down")
	} else {
		fmt.Fprintf(out, "VPN: up on %s", status.Interface)
		if status.Address != "" {
			fmt.Fprintf(out, " (%s)", status.Address)
		}
		fmt.Fprintln(out)
	}

	switch {
	case status.Session.LoginRequired:
		fmt.Fprintln(out, "Session: expired - run 'runos vpn up' to sign in again")
	case status.Session.Present:
		fmt.Fprintf(out, "Session: valid until %s (%s from now)\n",
			status.Session.ExpiresAt.Local().Format("2006-01-02 15:04"),
			roundDuration(time.Until(status.Session.ExpiresAt)))
	default:
		fmt.Fprintln(out, "Session: none - run 'runos vpn up'")
	}

	if status.DNS.Available {
		fmt.Fprintf(out, "Private DNS: available (%s)\n", status.DNS.Mode)
	} else if status.DNS.Error != "" {
		fmt.Fprintf(out, "Private DNS: unavailable - %s\n", status.DNS.Error)
	} else {
		fmt.Fprintln(out, "Private DNS: unavailable")
	}

	// A DAEMON OLDER THAN THIS CLI IS THE COMMONEST CAUSE OF AN EMPTY FIELD ABOVE. The daemon
	// serialises what ITS build knows, so a field added after it was started decodes as a zero
	// value here, and a zero value reads as a real answer rather than as a missing one.
	//
	// Measured 2026-08-19: daemon dev-2026-08-18T14:56:07Z against CLI dev-2026-08-18T19:40:41Z
	// printed "Private DNS: unavailable" with no reason while split DNS was WORKING. The daemon
	// had written /etc/resolver/<zone>, scutil showed the zone against the tunnel resolver, and
	// the zone resolved to private addresses. Every failure path in the daemon sets a Mode and an
	// Error, so an empty pair cannot come from a daemon that answered at all: it is the shape of a
	// field the running build never sent. Nothing said so, and the reader debugs DNS instead.
	if status.Running && status.Version != "" && status.Version != version.Version {
		fmt.Fprintf(out, "\nThe VPN service is running an older build than this CLI (%s against %s).\n",
			status.Version, version.Version)
		fmt.Fprintln(out, "Anything it does not know reads as empty above. Run 'runos vpn restart' to load the current build.")
	}

	if len(status.Clusters) == 0 {
		return nil
	}
	fmt.Fprintln(out)
	for _, c := range status.Clusters {
		fmt.Fprintf(out, "  %s\n", clusterStatusLine(c))
	}
	if note := peeringNote(status.Clusters); note != "" {
		fmt.Fprintf(out, "\n%s\n", note)
	}
	return nil
}

// peeringNote says what a peering does NOT give the device: a cluster peered with a connected
// one is still neither routed nor steered for DNS until the device connects to it too (peering
// is never transit, and only connected clusters' zones are split). Measured on the Mac: the
// peered cluster's names answered publicly and its overlay address was not routed. Silent when
// no connected cluster is peered with a disconnected one.
func peeringNote(clusters []vpn.ClusterStatus) string {
	connected := map[string]bool{}
	for _, c := range clusters {
		if c.Connected {
			connected[c.CID] = true
		}
	}
	var lines []string
	for _, c := range clusters {
		if !c.Connected {
			continue
		}
		for _, peer := range c.PeeredWith {
			if !connected[peer] {
				lines = append(lines, fmt.Sprintf("  %s is peered with %s, but %s's names and addresses stay public and unrouted here until you 'runos vpn connect %s'.",
					c.CID, peer, peer, peer))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Peerings:\n" + strings.Join(lines, "\n")
}

// clusterStatusLine is one cluster's summary: name, connection state, and either the live
// handshake/traffic or the reason it is not reachable.
func clusterStatusLine(c vpn.ClusterStatus) string {
	label := fmt.Sprintf("%-8s %s", c.CID, c.Name)
	switch {
	case c.Connected && c.PeerUp && !c.LastHandshake.IsZero():
		return fmt.Sprintf("%s  connected, last handshake %s ago, %s down / %s up",
			label, roundDuration(time.Since(c.LastHandshake)), bytesHuman(c.RxBytes), bytesHuman(c.TxBytes))
	case c.Connected && c.PeerUp:
		return fmt.Sprintf("%s  connected, no handshake yet", label)
	case c.Connected && !c.Reachable:
		// A dead state, and not one waiting fixes: the device is in the connected set for a
		// cluster that cannot serve it. Name the way out, or the line is just a complaint.
		return fmt.Sprintf("%s  connected but unreachable: %s. Nothing routes here; 'runos vpn disconnect %s' to clear it",
			label, c.Reason, c.CID)
	case c.Connected:
		return fmt.Sprintf("%s  connecting...", label)
	case !c.Reachable:
		// NOT "available". "Available" is an offer to connect, and connecting to a cluster with no
		// VPN server only buys the dead state above. The reason carries the whole explanation.
		return fmt.Sprintf("%s  cannot connect: %s", label, c.Reason)
	default:
		return fmt.Sprintf("%s  available (not connected)", label)
	}
}

// roundDuration trims a duration to a readable whole unit.
func roundDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Second).String()
}

// bytesHuman renders a byte count in binary units.
func bytesHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
