package cmd

import "testing"

/*
An "update" must never be a DOWNGRADE.

`Manager.Update()` treated any difference as needing an install, so a build NEWER than the latest
release was replaced by the older one. That was harmless while every local build claimed to be
0.1.0, which is always behind. It stopped being harmless when local builds began reporting the
version under development: the very next cycle, every developer's machine would report an update
waiting and installing it would take them backwards.

Newer is newer. Anything else is not an update.
*/
func TestAnUpdateIsNeverADowngrade(t *testing.T) {
	for _, tc := range []struct {
		name              string
		installed, latest string
		want              bool
	}{
		{name: "behind the release", installed: "0.3.0", latest: "0.4.0", want: true},
		{name: "on the release", installed: "0.4.0", latest: "0.4.0", want: false},
		{name: "AHEAD of the release, the case that would downgrade", installed: "0.5.0", latest: "0.4.0", want: false},
		{name: "patch behind", installed: "0.4.0", latest: "0.4.1", want: true},
		{name: "nothing installed", installed: "", latest: "0.4.0", want: false},
		{name: "latest unknown", installed: "0.4.0", latest: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := desktopUpdateAvailable(tc.installed, tc.latest); got != tc.want {
				t.Errorf("desktopUpdateAvailable(%q, %q) = %v, want %v", tc.installed, tc.latest, got, tc.want)
			}
		})
	}
}
