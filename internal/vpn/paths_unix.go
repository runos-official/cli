//go:build !windows

package vpn

// SocketPath is the daemon's control socket on a unix host.
const SocketPath = "/var/run/runos-vpn.sock"

// StateDir is where the daemon keeps its state on a unix host.
const StateDir = "/var/lib/runos-vpn"

// DaemonLogPath is the daemon's own log, world readable so a person can read it without root. It is
// distinct from the launchd output file on macOS, which launchd owns and the daemon cannot rotate.
const DaemonLogPath = "/var/log/runos-vpn-daemon.log"
