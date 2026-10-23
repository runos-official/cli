//go:build !windows

package vpn

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive flock without waiting. The lock belongs to the open file
// description, so a second open of the same file in the same process conflicts too.
func tryLockFile(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

func unlockFile(file *os.File) { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
