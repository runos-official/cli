//go:build !windows

package vpn

// SecureStateDir has nothing to do on unix: SaveState creates the directory 0700 and the file 0600.
func SecureStateDir(string) error { return nil }
