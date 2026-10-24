package vpn

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/testguard"
)

/*
clientSocketPath is the socket a Client dials. In a test binary the real daemon is never one of the
answers: an empty path or SocketPath becomes a path that cannot exist, so Call returns the ordinary
NotRunningError and a forgotten --socket is harmless. A release binary gets SocketPath as before.

The replacement sits under the test binary's own file, which is a regular file, so no directory
and no socket can ever exist at that path.
*/
func clientSocketPath(path string) string {
	if path == "" {
		path = SocketPath
	}
	if testguard.Active() && path == SocketPath {
		return filepath.Join(os.Args[0], "no-daemon-in-tests.sock")
	}
	return path
}

// refuseRealSocketInTest stops Serve from replacing the live daemon's socket from a test binary.
func refuseRealSocketInTest(path string) error {
	if testguard.Active() && path == SocketPath {
		return fmt.Errorf("refusing to serve on the real daemon socket %s from a test binary; pass a temp socket path", SocketPath)
	}
	return nil
}
