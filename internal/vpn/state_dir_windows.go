//go:build windows

package vpn

import (
	"fmt"
	"os"
	"path/filepath"
)

// SecureStateDir creates the state directory and applies the rules in acl_args.go to it and to any
// state file an earlier build left there. Call it before anything is written.
func SecureStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if out, err := run("icacls", stateDirArgs(dir)...); err != nil {
		return fmt.Errorf("restrict the state directory: %w: %s", err, out)
	}
	for _, name := range []string{"state.json", "state.json.tmp"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if out, err := run("icacls", privateFileArgs(path)...); err != nil {
			return fmt.Errorf("restrict %s: %w: %s", name, err, out)
		}
	}
	return nil
}
