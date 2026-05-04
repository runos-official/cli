//go:build !windows

package apps

import "syscall"

// nofollowOpenFlag is OR'd into os.OpenFile's flag argument when the
// caller wants to refuse to write through a symlink. Combined with
// O_TRUNC, an existing regular file gets clobbered (intended); an
// existing symlink at the same path returns ELOOP and the caller
// surfaces it as an error rather than silently following.
//
// On Unix this is the kernel-level O_NOFOLLOW flag, atomic with the
// open() syscall.
const nofollowOpenFlag = syscall.O_NOFOLLOW
