package vpn

import "testing"

/*
The self-heal, and the far more important question of what it must refuse to touch.

This runs as ROOT and changes who may control the VPN, so the cases below are weighted towards the
ones where doing nothing is correct. A heal that widens access somebody chose deliberately is worse
than the defect it fixes: the defect locks a person out and is visible, and a silent widening is
neither.
*/

func gids(m map[string]int) func(string) (int, bool) {
	return func(name string) (int, bool) {
		gid, ok := m[name]
		return gid, ok
	}
}

func TestTheHealFixesTheReportedFailure(t *testing.T) {
	// MEASURED on a Mac: `wheel` is GID 0 and holds only root; an ordinary admin account is in
	// `staff` (20) and `admin` (80). This is the state both reporters were in.
	got := usableSocketGroup("wheel", "darwin", false, gids(map[string]int{"wheel": 0, "admin": 80}), func() string { return "admin" })

	if got != "admin" {
		t.Errorf("usableSocketGroup(wheel, darwin) = %q, want admin", got)
	}
}

/*
Everything this must leave exactly as it found it.

Each row is a way the heal could do harm rather than good, and each is a real configuration rather
than a hypothetical one.
*/
func TestTheHealRefusesToTouchAnythingElse(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		goos       string
		explicit   bool
		groups     map[string]int
		admin      string
		want       string
	}{
		{
			/*
			 THE CASE THAT MATTERS MOST, and the one this change originally got wrong.

			 On darwin `wheel` is the ONLY spelling of a root-only group: there is no group named
			 `root`. So an operator running `vpn install --socket-group wheel` to keep the control
			 socket root-only was having it rewritten to `admin` on every daemon start. Access went
			 from {root} to {root and every admin account}, and reaching this socket is the WHOLE
			 authorisation: it is enough to drop the tunnel, sign the machine out, and destroy the
			 enrolled device key, with no password prompt.

			 The GID cannot tell a derived group from a chosen one, so the installer records which
			 it was.
			*/
			name: "a group a person NAMED is never overruled", configured: "wheel", goos: "darwin",
			explicit: true, groups: map[string]int{"wheel": 0, "admin": 80}, admin: "admin", want: "wheel",
		},
		{
			// EMPTY MEANS NO CHOWN, so the socket stays root-only. That is a posture an operator
			// may want, and healing it would hand control of the VPN to a group nobody asked for.
			name: "an empty group stays empty", configured: "", goos: "darwin",
			groups: map[string]int{}, admin: "admin", want: "",
		},
		{
			// The ordinary, healthy macOS install. Untouched.
			name: "the normal macOS group", configured: "staff", goos: "darwin",
			groups: map[string]int{"staff": 20}, admin: "admin", want: "staff",
		},
		{
			// LINUX `wheel` IS NOT macOS `wheel`. There it is GID 10 and genuinely holds the
			// sudoers. Rewriting it would break a correct install.
			name: "linux wheel is a real admin group", configured: "wheel", goos: "linux",
			groups: map[string]int{"wheel": 10}, admin: "sudo", want: "wheel",
		},
		{
			// A deliberate root-only socket on Linux. This cannot tell it from the defect, so it
			// does not try: Linux was never the platform that broke.
			name: "linux root-only is left alone", configured: "root", goos: "linux",
			groups: map[string]int{"root": 0}, admin: "sudo", want: "root",
		},
		{
			// An operator who named a group knows their machine, and this does not.
			name: "a group somebody chose", configured: "developers", goos: "darwin",
			groups: map[string]int{"developers": 501}, admin: "admin", want: "developers",
		},
		{
			// A group the machine does not have. Changing it would be a guess on top of a guess,
			// and leaving it means the socket keeps whatever group it already had.
			name: "an unknown group", configured: "nosuchgroup", goos: "darwin",
			groups: map[string]int{}, admin: "admin", want: "nosuchgroup",
		},
		{
			// Nothing to heal TO. Keeping the configured group changes nothing, which beats
			// inventing an answer.
			name: "no administrators group exists", configured: "wheel", goos: "darwin",
			groups: map[string]int{"wheel": 0}, admin: "", want: "wheel",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := usableSocketGroup(tc.configured, tc.goos, tc.explicit, gids(tc.groups), func() string { return tc.admin })
			if got != tc.want {
				t.Errorf("usableSocketGroup(%q, %s, explicit=%v) = %q, want %q", tc.configured, tc.goos, tc.explicit, got, tc.want)
			}
		})
	}
}

// macOS administrators are in `admin`. `wheel` exists there and holds only root, which is the whole
// defect, so it must never be offered as a candidate.
func TestMacOSCandidatesAreAdminAndNeverWheel(t *testing.T) {
	got := AdminGroupCandidates("darwin")

	if len(got) != 1 || got[0] != "admin" {
		t.Errorf("AdminGroupCandidates(darwin) = %v, want [admin]", got)
	}
}

// Linux keeps sudoers in `sudo` (Debian) or `wheel` (RHEL, Arch). Both are tried because a
// candidate the box does not have is no use.
func TestLinuxCandidatesCoverBothConventions(t *testing.T) {
	got := AdminGroupCandidates("linux")

	seen := map[string]bool{}
	for _, name := range got {
		seen[name] = true
	}
	if !seen["sudo"] || !seen["wheel"] {
		t.Errorf("AdminGroupCandidates(linux) = %v, want both sudo and wheel", got)
	}
}

func TestOnlyAGroupTheMachineHasIsChosen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []string
		present    map[string]bool
		want       string
	}{
		{"the preferred one", []string{"sudo", "wheel"}, map[string]bool{"sudo": true, "wheel": true}, "sudo"},
		{"falls through", []string{"sudo", "wheel"}, map[string]bool{"wheel": true}, "wheel"},
		{"none exist", []string{"sudo", "wheel"}, map[string]bool{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FirstExistingGroup(tc.candidates, func(n string) bool { return tc.present[n] })
			if got != tc.want {
				t.Errorf("FirstExistingGroup(%v) = %q, want %q", tc.candidates, got, tc.want)
			}
		})
	}
}

/*
One definition of "root's own group", because the installer and the daemon both refuse it and a
second opinion is how they drift apart.

GID 0 owns `wheel` on macOS and `root` on Linux.
*/
func TestOnlyGIDZeroCountsAsRootsGroup(t *testing.T) {
	for _, gid := range []string{"0"} {
		if !IsRootGroup(gid) {
			t.Errorf("IsRootGroup(%q) = false, want true", gid)
		}
	}
	// staff (20), admin (80), a Linux wheel (10), a user's own group, and anything unreadable.
	for _, gid := range []string{"20", "80", "10", "501", "", "not-a-number"} {
		if IsRootGroup(gid) {
			t.Errorf("IsRootGroup(%q) = true, want false", gid)
		}
	}
}
