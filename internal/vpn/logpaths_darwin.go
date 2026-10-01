//go:build darwin

package vpn

// legacyLogPaths are where an older daemon on this OS wrote. launchd's StandardOutPath names this
// file in the plist, and it still receives stderr beside the daemon's own bounded log.
func legacyLogPaths() []string { return []string{"/var/log/runos-vpn.log"} }
