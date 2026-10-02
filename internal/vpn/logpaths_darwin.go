//go:build darwin

package vpn

// legacyLogPaths are where an older daemon on this OS wrote. An older install's plist names this file
// as launchd's StandardOutPath, so a machine that has not been reinstalled still receives stderr
// there beside the daemon's own bounded log. A fresh install points launchd at /dev/null instead.
func legacyLogPaths() []string { return []string{"/var/log/runos-vpn.log"} }
