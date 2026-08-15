package vpn

import "os/exec"

// run executes a command and returns its combined output. Every platform renderer shells out
// through this one helper so a failure message always carries the tool's own words.
func run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
