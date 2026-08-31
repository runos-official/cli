//go:build windows

package vpn

// socketGroupName has no meaning on Windows: an AF_UNIX socket there is reached through the file's
// ACL, not a POSIX group, and grantSocketAccess opens it to local users that way. Empty, so a
// permission message describes the group rather than naming one that does not exist.
func socketGroupName(string) string { return "" }
