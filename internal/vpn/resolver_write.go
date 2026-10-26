package vpn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kept out of the darwin build tag, like managed_marker.go, so the Linux and Windows runs compile
// and test the same writer.

// resolverTempPrefix starts the name of every temp file the atomic writer makes. It is the only
// thing the sweep for stale temp files matches, so nothing else in the directory is ever removed.
const resolverTempPrefix = ".runos-vpn-resolver-"

// resolverFS is the part of the file system the atomic writer touches, so a test can make a step
// fail and prove the cleanup.
type resolverFS interface {
	CreateTemp(dir, pattern string) (resolverTempFile, error)
	Rename(oldPath, newPath string) error
	Remove(path string) error
}

type resolverTempFile interface {
	Name() string
	Write(p []byte) (int, error)
	Chmod(mode os.FileMode) error
	Close() error
}

type osResolverFS struct{}

func (osResolverFS) CreateTemp(dir, pattern string) (resolverTempFile, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (osResolverFS) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
func (osResolverFS) Remove(path string) error             { return os.Remove(path) }

/*
writeFileAtomic puts data at dest so that no reader ever sees an empty or partial file there.

The content goes to a temp file in tmpDir first, and rename moves it into place. tmpDir must be on
the same file system as dest's directory (rename is only atomic there) and must NOT be the directory
a service watches: macOS configd watches /etc/resolver, and a temp file created inside it announces
an empty file before the content exists. For /etc/resolver/<zone> the caller passes /etc.

The temp file is removed on every failure, so an error leaves nothing behind. There is no fsync: a
crash that loses the content also loses the daemon, and the next start sweeps every managed file.
*/
func writeFileAtomic(fs resolverFS, tmpDir, dest string, data []byte, perm os.FileMode) (err error) {
	tmp, err := fs.CreateTemp(tmpDir, resolverTempPrefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create a temporary file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = fs.Remove(name)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write the temporary file: %w", err)
	}
	// CreateTemp makes the file 0600; macOS reads resolver files as the user that asks, so 0644.
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("set the file mode: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close the temporary file: %w", err)
	}
	if err = fs.Rename(name, dest); err != nil {
		return fmt.Errorf("move the file into place: %w", err)
	}
	return nil
}

// removeStaleResolverTemps deletes temp files an earlier, killed daemon left in dir. Best effort:
// a file that cannot be removed is left for the next sweep.
func removeStaleResolverTemps(fs resolverFS, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), resolverTempPrefix) {
			_ = fs.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
