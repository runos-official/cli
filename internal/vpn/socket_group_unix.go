//go:build !windows

package vpn

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// socketGroupName reads the group actually owning the socket, so a permission message can name the
// fact rather than guess it. Empty when it cannot be read, in which case the message describes the
// group instead of naming it.
//
// UNIX ONLY, and in its own file for that reason: `syscall.Stat_t` does not exist on Windows, and
// putting this in the untagged socket.go broke every Windows build. Windows has its own
// grantSocketAccess and reaches an AF_UNIX socket by file ACL rather than by group.
func socketGroupName(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	grp, err := user.LookupGroupId(strconv.FormatUint(uint64(stat.Gid), 10))
	if err != nil {
		return ""
	}
	return grp.Name
}
