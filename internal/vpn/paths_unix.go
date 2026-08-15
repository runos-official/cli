//go:build !windows

package vpn

// SocketPath is the daemon's control socket on a unix host.
const SocketPath = "/var/run/runos-vpn.sock"

// StateDir is where the daemon keeps its state on a unix host.
const StateDir = "/var/lib/runos-vpn"
