/*
Package testguard keeps a test binary off the real machine.

The CLI drives three things a test must never reach: the VPN daemon's control socket, the
config directory under the person's home, and the OS credential store. Each opener asks this
package first. In a release binary every function here answers "allowed", so the shipped
behaviour does not change.

WHY IT EXISTS. Running `go test ./...` on a developer machine took that machine's real VPN tunnel
down and ended its VPN session, twice. An account-change test passed no --socket, so the CLI
dialled the real root daemon and sent it `down`. A test that forgets its isolation must fail
loudly, never act on the real machine.
*/
package testguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Active reports whether this process is a `go test` binary. It is false in a release binary.
func Active() bool { return testing.Testing() }

// UnderTempDir reports whether path lies inside the system temp directory, where t.TempDir puts
// every test directory. It compares both the cleaned and the symlink-resolved forms, because the
// temp directory on macOS is reached through the /var -> /private/var link.
func UnderTempDir(path string) bool {
	if path == "" {
		return false
	}
	tmp := os.TempDir()
	if within(filepath.Clean(path), filepath.Clean(tmp)) {
		return true
	}
	realPath, err1 := filepath.EvalSymlinks(path)
	realTmp, err2 := filepath.EvalSymlinks(tmp)
	return err1 == nil && err2 == nil && within(realPath, realTmp)
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
