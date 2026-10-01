//go:build !darwin

package vpn

// legacyLogPaths: only the launchd plist ever named a log file, so there is no older location here.
func legacyLogPaths() []string { return nil }
