package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/runos-official/cli/internal/testguard"
)

/*
HomeDir is the person's home directory, and the only place the CLI asks the OS for it. Every path
under home (~/.runos, the Desktop app, an editor's MCP config) starts here.

In a test binary it refuses a home outside the temp directory. A test that forgot
t.Setenv("HOME", t.TempDir()) used to read the developer's real config and then act on the real
sign-in; now it fails with an error that names the fix. A release binary is unaffected.
*/
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if testguard.Active() && !testguard.UnderTempDir(home) {
		return "", fmt.Errorf("test binary: HOME=%s is not a temp directory; a test must call t.Setenv(\"HOME\", t.TempDir()) before it reaches the RunOS config", home)
	}
	return home, nil
}

// Dir is the CLI's config directory (~/.runos). Exported because the MCP toolset cache, the
// manifest cache and the update cache live in it.
func Dir() (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName), nil
}
