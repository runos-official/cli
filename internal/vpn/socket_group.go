package vpn

import (
	"os/user"
	"runtime"
	"strconv"
)

/*
Which group may open the control socket, and how a machine heals itself when that group is wrong.

REPORTED 2026-08-31 by two macOS users independently. After installing, every command answered "the
RunOS VPN service is not running. Run 'sudo runos vpn install' first." The service was running: their
socket was `root:wheel`, they were in `admin`, and connect() returned EACCES.

`wheel` was never written anywhere. The installer asks for the primary group of the person
installing, which is right, and used to fall back to the effective user when SUDO_USER was unset.
Under `sudo -i`, `sudo su -`, a root shell or a provisioning script, that is root, whose primary
group is `wheel` on macOS and `root` on Linux. Both are GID 0, and a socket owned by GID 0 reaches
nobody except root, which is the exact opposite of the point.

TWO PLACES FIX IT, because one of them alone would leave the people already broken.

  - The installer no longer derives anything from root. That fixes new installs.
  - The DAEMON checks the group it was told to use, every time it starts, and overrides it when it
    is unusable. That fixes installs that already exist, without anybody editing a plist, because
    the daemon runs as root and can chown the socket itself.

The second is the one that matters for somebody already stuck: their plist still says `wheel`, and
one `sudo runos vpn restart`, or the next reboot, puts the socket right.
*/

// rootGID owns `wheel` on macOS and `root` on Linux. Any group with this id contains root and, by
// default, nobody else, so handing it the socket makes the socket unreachable by every person the
// service exists for.
const rootGID = 0

/*
AdminGroupCandidates names where a machine keeps its administrators, in preference order.

macOS puts them in `admin`. It also HAS a `wheel`, holding only root, which is what let the reported
failure look like a plausible answer while reaching nobody. Linux keeps its sudoers in `sudo` on
Debian and Ubuntu and `wheel` on RHEL, Fedora and Arch, so both are tried. `wheel` really does hold
administrators on Linux, which is why it is right there and wrong on darwin.
*/
func AdminGroupCandidates(goos string) []string {
	if goos == "darwin" {
		return []string{"admin"}
	}
	return []string{"sudo", "wheel"}
}

// FirstExistingGroup returns the first candidate the machine actually has. Empty when it has none:
// the socket then stays root-only, which is at least honest, rather than being handed to a group
// that is not there.
func FirstExistingGroup(candidates []string, exists func(string) bool) string {
	for _, name := range candidates {
		if exists(name) {
			return name
		}
	}
	return ""
}

// GroupExists reports whether the machine has a group by this name.
func GroupExists(name string) bool {
	_, err := user.LookupGroup(name)
	return err == nil
}

// AdminGroup is the administrators group this machine actually has, or empty.
func AdminGroup() string {
	return FirstExistingGroup(AdminGroupCandidates(runtime.GOOS), GroupExists)
}

/*
usableSocketGroup is the self-heal, and it is deliberately the narrowest thing that fixes the
reported failure.

IT HEALS EXACTLY ONE CASE: a configured group that is GID 0, on darwin. That is `wheel`, it holds
only root, and it is never a sensible owner for a socket whose entire purpose is to be reached
without sudo. It is also the only value the old fallback could produce on a Mac.

WHAT IT MUST NEVER DO IS WIDEN ACCESS SOMEBODY CHOSE:

  - An EMPTY group is left empty. Empty means the caller did not ask for a chown, so the socket
    stays root-only, and that is a real posture an operator may want. Healing it would hand control
    of the VPN to a group nobody asked for.
  - LINUX IS LEFT ALONE ENTIRELY. `wheel` there is GID 10 and genuinely holds administrators, and a
    deliberate `--socket-group root` means root-only. This cannot tell that apart from the defect,
    so on Linux it does not try; the Linux install path was never the one that broke.
  - ANY OTHER GROUP IS HONOURED. An operator who passed `--socket-group developers` gets
    `developers`. They know their machine and this does not.

On darwin the heal also cannot widen access relative to a healthy install: the ordinary macOS
default is the installing user's primary group, `staff`, which contains every local account, and
`admin` is a strict subset of that.
*/
func usableSocketGroup(configured, goos string, groupExplicit bool, gidOf func(string) (int, bool), adminGroup func() string) string {
	/*
	 A GROUP SOMEBODY NAMED IS NEVER OVERRULED.

	 The GID alone cannot tell a derived group from a chosen one, and on darwin `wheel` is the ONLY
	 spelling of a root-only group: there is no group named `root`. So an operator running
	 `vpn install --socket-group wheel` to keep the socket root-only was having it rewritten to
	 `admin` on every daemon start, taking access from {root} to {root and every admin account}.
	 The socket is the whole authorisation for the daemon: reaching it is enough to drop the tunnel,
	 sign the machine out and destroy the enrolled device key.

	 The installer records which it was. An absent marker means derived, so every machine installed
	 by an older build still heals.
	*/
	if groupExplicit || configured == "" || goos != "darwin" {
		return configured
	}
	gid, ok := gidOf(configured)
	if !ok || gid != rootGID {
		return configured
	}
	// GID 0 on darwin: `wheel`, root-only, and unreachable by the person who installed this.
	if healed := adminGroup(); healed != "" {
		return healed
	}
	// No administrators group to fall back to. Keeping the configured one changes nothing, which
	// is better than guessing.
	return configured
}

func groupGID(name string) (int, bool) {
	grp, err := user.LookupGroup(name)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}
