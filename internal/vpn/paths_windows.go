//go:build windows

package vpn

// SocketPath is the daemon's control socket on Windows: an AF_UNIX socket file (supported since
// Windows 10 1803, and by Go's "unix" network), kept under ProgramData because the daemon runs
// as LocalSystem, whose profile is not a place a person's CLI can reach.
const SocketPath = `C:\ProgramData\RunOS\vpn\runos-vpn.sock`

// StateDir is where the daemon keeps its state on Windows.
const StateDir = `C:\ProgramData\RunOS\vpn`

// DaemonLogPath is the daemon's own log. The directory is closed to everyone but SYSTEM and
// Administrators (acl_args.go); the log file alone is granted to Users, as the socket is.
const DaemonLogPath = StateDir + `\daemon.log`
