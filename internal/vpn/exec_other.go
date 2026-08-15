//go:build !darwin

package vpn

import "os/exec"

// run executes a command and returns its combined output. macOS has its own copy in
// platform_darwin.go; this serves the Linux and Windows platforms.
func run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
