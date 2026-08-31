package cmd

import (
	"os/user"
	"runtime"
	"testing"

	"github.com/runos-official/cli/internal/vpn"
)

/*
The control socket's group must contain a person, and deriving it from root guarantees it does not.

REPORTED 2026-08-31 by two macOS users, independently. Both installed the VPN service, and every
command afterwards answered:

	the RunOS VPN service is not running (/var/run/runos-vpn.sock).
	Run 'sudo runos vpn install' first.

The service was running the whole time. Their control socket was `root:wheel`, they were in `admin`,
and `connect()` returned EACCES. Reinstalling, which is what the message told them to do, re-ran the
same computation and produced the same socket, so the advice was a loop.

WHERE `wheel` CAME FROM. Not a hardcoded constant: it has never appeared in this repository. The
installer asks for the primary group of the user installing, which is right, and falls back to
`user.Current()` when SUDO_USER is unset. Under `sudo -i`, `sudo su -`, a root shell or a
provisioning script, SUDO_USER is unset and `user.Current()` is root, whose primary group is `wheel`
on macOS and `root` on Linux.

Neither of those contains any of the people who will run the CLI afterwards. Root's group is the one
answer that is guaranteed wrong, because the entire point of the setting is that the installing
person reaches the socket without sudo.

MEASURED on the machine this was written on: `id -gn root` is `wheel`, and an ordinary admin account
is in `staff` and `admin`, not `wheel`. An install with SUDO_USER set produced `staff` and worked.
*/

func TestTheAdminGroupIsNeverRootsOwnGroup(t *testing.T) {
	for _, name := range vpn.AdminGroupCandidates(runtime.GOOS) {
		if name == "wheel" && runtime.GOOS == "darwin" {
			t.Errorf("`wheel` holds only root on macOS, so a socket owned by it reaches nobody")
		}
		if name == "root" {
			t.Errorf("`root` is root's own group; a socket owned by it reaches nobody")
		}
	}
}

// macOS puts administrators in `admin`. `wheel` exists but holds only root by default, which is the
// whole defect.
func TestMacOSAdministratorsAreInAdmin(t *testing.T) {
	got := vpn.AdminGroupCandidates("darwin")

	if len(got) == 0 || got[0] != "admin" {
		t.Errorf("vpn.AdminGroupCandidates(darwin) = %v, want admin first", got)
	}
}

// Linux keeps its sudoers in `sudo` (Debian) or `wheel` (RHEL, Arch). Both are tried, because a
// candidate that does not exist on the box is no use.
func TestLinuxTriesBothSudoerGroups(t *testing.T) {
	got := vpn.AdminGroupCandidates("linux")

	found := map[string]bool{}
	for _, name := range got {
		found[name] = true
	}
	if !found["sudo"] || !found["wheel"] {
		t.Errorf("vpn.AdminGroupCandidates(linux) = %v, want both sudo and wheel", got)
	}
}

/*
Only a group the machine actually has is any use, so the candidates are tried in order.

An empty answer is the honest one when none exists: the socket stays root-only, which is at least
true, rather than being handed to a group that is not there.
*/
func TestTheFirstGroupTheMachineActuallyHasIsChosen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []string
		present    map[string]bool
		want       string
	}{
		{
			name:       "the preferred one exists",
			candidates: []string{"sudo", "wheel"},
			present:    map[string]bool{"sudo": true, "wheel": true},
			want:       "sudo",
		},
		{
			name:       "falls through to the second",
			candidates: []string{"sudo", "wheel"},
			present:    map[string]bool{"wheel": true},
			want:       "wheel",
		},
		{
			name:       "none of them exist",
			candidates: []string{"sudo", "wheel"},
			present:    map[string]bool{},
			want:       "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := vpn.FirstExistingGroup(tc.candidates, func(name string) bool { return tc.present[name] })
			if got != tc.want {
				t.Errorf("vpn.FirstExistingGroup(%v) = %q, want %q", tc.candidates, got, tc.want)
			}
		})
	}
}

/*
The installer's own rule, which had no test at all.

This function IS the root cause. It asks for the primary group of the person installing, via
SUDO_USER, and used to fall back to `user.Current()` when SUDO_USER was unset. Under `sudo -i`,
`sudo su -`, a root shell or a provisioning script that is root, whose primary group is `wheel` on
macOS and `root` on Linux. Both are GID 0, so the socket reached nobody but root.

MEASURED: restoring the old body left all 22 packages green, because every test written for this
defect exercised the helpers and never the decision.

The two cases are driven through the environment because that is the whole of the input: SUDO_USER
present means a person is behind the sudo and their group is the answer; absent means there is no
person to ask about, and root's own group is the one answer guaranteed wrong.
*/
func TestTheInstallerNeverDerivesAGroupFromRoot(t *testing.T) {
	t.Run("no SUDO_USER means the administrators group, never root's", func(t *testing.T) {
		t.Setenv("SUDO_USER", "")

		got := socketGroupForInstall()

		if got == "wheel" || got == "root" {
			t.Fatalf("socketGroupForInstall() = %q: root's own group reaches nobody", got)
		}
		if want := vpn.AdminGroup(); got != want {
			t.Errorf("socketGroupForInstall() = %q, want the administrators group %q", got, want)
		}
	})

	t.Run("SUDO_USER names the person whose CLI must reach the socket", func(t *testing.T) {
		current, err := user.Current()
		if err != nil {
			t.Skip("no current user")
		}
		t.Setenv("SUDO_USER", current.Username)

		got := socketGroupForInstall()

		primary, err := user.LookupGroupId(current.Gid)
		if err != nil {
			t.Skip("cannot resolve the current user's primary group")
		}
		if got != primary.Name {
			t.Errorf("socketGroupForInstall() = %q, want the sudoer's primary group %q", got, primary.Name)
		}
	})
}
