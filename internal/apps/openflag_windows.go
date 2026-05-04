//go:build windows

package apps

// nofollowOpenFlag is the Windows stub for the symlink-refusal flag.
// Windows does not expose syscall.O_NOFOLLOW (the concept maps onto
// Windows reparse points and a different file-open flag space we
// don't currently target), so callers fall back to the default open
// behaviour. The symlink-refusal protection in apps_pull is therefore
// degraded on Windows; the stable platform target for the CLI
// continues to be Unix runners (CI, prod servers), where the
// O_NOFOLLOW path is honoured atomically.
const nofollowOpenFlag = 0
