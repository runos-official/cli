package vpn

// service is the OS-specific installer for the daemon: the launchd LaunchDaemon on macOS, a
// systemd unit on Linux, a Windows service on Windows. `runos vpn install` needs admin once
// (decision 5) because creating the service and, later, a network interface does on every OS.
//
// The installer is deliberately tiny and declarative: it writes one service definition that runs
// `runos vpn daemon`, loads it, and reports exactly what it wrote so `uninstall` can undo it.
type service interface {
	// Install writes and loads the service so `runos vpn daemon` runs as root. execPath is the
	// absolute path of the running runos binary; socketGroup is the group that may reach the
	// control socket (the installing user's primary group), so their CLI needs no sudo.
	Install(execPath, socketGroup string) error
	// Uninstall stops and removes the service and the socket. Idempotent.
	Uninstall() error
	// Running reports whether the service is loaded.
	Running() (bool, error)
	// Describe returns one plain sentence saying how the service runs on this OS, for the install
	// command to print under its headline. No paths: a person installing a VPN wants to know it
	// runs in the background and starts at boot, not where the unit file lives.
	Describe() string
}
