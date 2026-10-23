package cmd

import (
	"time"

	"github.com/runos-official/cli/internal/vpn"
	"github.com/spf13/cobra"
)

// daemonStatusTimeout bounds the status question `runos update` asks the daemon. The ordinary
// socket call waits up to 120 s because an `up` builds a tunnel; a status answer takes
// milliseconds, so a wedged daemon must cost an update two seconds, not two minutes. A variable
// for tests.
var daemonStatusTimeout = 2 * time.Second

// vpnTunnelUpdateNotice is what `runos update` says BEFORE it replaces the program when the VPN is
// connected. Empty when no tunnel is up, so a machine that never used the VPN reads nothing.
//
// Every sentence is true of the code: the update renames a new file over the old one and never
// signals the daemon (internal/update replaceBinary), the loaded daemon keeps running its old
// build, and a restart stops the tunnel and resumes the stored session (Daemon.Resume) with the
// connected clusters Conductor holds for the device.
func vpnTunnelUpdateNotice(tunnelUp bool) string {
	if !tunnelUp {
		return ""
	}
	return "\nThe VPN is connected. This update does not change the running tunnel.\n" +
		"The VPN service keeps its old build until you restart it with: sudo runos vpn restart\n" +
		"That restart cuts the tunnel for a few seconds. It then connects again by itself,\n" +
		"without a new sign-in, while your 24 hour VPN session is valid.\n\n"
}

// daemonStatus asks the daemon for its status. Nil when it cannot be reached or is too old to say,
// which callers treat as "no evidence".
func daemonStatus(cmd *cobra.Command) *vpn.Status {
	resp, err := vpnSocketClient(cmd).CallWithin(vpn.Request{Op: vpn.OpStatus}, daemonStatusTimeout)
	if err != nil {
		return nil
	}
	return resp.Status
}

// tunnelState is what the daemon says about the connection at the moment of the update.
type tunnelState int

const (
	// tunnelDown: no tunnel, or no answer from the daemon.
	tunnelDown tunnelState = iota
	// tunnelLive: a tunnel is up and its session is valid, so a restarted service resumes it.
	tunnelLive
	// tunnelLapsed: the interface is up but the session has ended, so a restart does not resume it.
	tunnelLapsed
)

func tunnelStateOf(status *vpn.Status) tunnelState {
	if status == nil || !status.Running {
		return tunnelDown
	}
	if status.Session.Present && !status.Session.LoginRequired {
		return tunnelLive
	}
	return tunnelLapsed
}
