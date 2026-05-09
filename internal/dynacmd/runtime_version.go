package dynacmd

import (
	"runtime"

	"github.com/runos-official/cli/version"
)

// cliRuntimeVersion returns the running CLI binary's version string. Used
// by the dynacmd executor to auto-fill `cli/version-check` when the user
// invokes the bare `runos cli version-check` (without --version), so the
// server can compute updateAvailable correctly. Mirrors the MCP wrapper's
// behaviour at internal/mcp/server.go.
func cliRuntimeVersion() string {
	return version.Version
}

// cliRuntimeOS returns the running CLI binary's OS slug ("darwin",
// "linux", "windows", ...). Same auto-injection rationale as
// cliRuntimeVersion.
func cliRuntimeOS() string {
	return runtime.GOOS
}
