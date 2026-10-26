package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/update"
	"github.com/runos-official/cli/version"
)

/*
RunOS Desktop runs `runos --version` on every refresh and does two things with the line:

  - It classifies it: a dev build ("dev" or "dev-...") is let through, a version below Desktop's
    floor is refused, and anything else must parse as MAJOR.MINOR.PATCH with an optional
    "-prerelease". A line it cannot parse stops the refresh and puts "cannot identify CLI version"
    in the menu.
  - It compares it, as a plain string, with the version the VPN daemon reports. A difference offers
    "Restart VPN", which asks for an administrator password.

So the output must be the bare version and nothing else, and the daemon must be started with that
same string. The three forms a build can have are what the release workflow, a release candidate and
`make local` produce.
*/

var desktopVersionForms = []struct{ name, version string }{
	{"release tag", "1.22.2"},
	{"pre-release tag", "1.23.0-rc.1"},
	{"dev-stamped build", "dev-2026-10-26T12:00:00Z"},
}

// desktopClassify is a copy of VersionComparator.compatibility in the Desktop app's
// RefreshCoordinator.swift: development, invalid, or supported (the floor is not modelled).
func desktopClassify(value string) string {
	v := strings.TrimSpace(value)
	if v == "dev" || strings.HasPrefix(v, "dev-") {
		return "development"
	}
	core, pre, hasPre := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return "invalid"
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return "invalid"
		}
	}
	if hasPre {
		if pre == "" || strings.Contains(pre, "..") || strings.HasPrefix(pre, ".") || strings.HasSuffix(pre, ".") ||
			strings.Trim(pre, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-") != "" {
			return "invalid"
		}
	}
	return "supported"
}

func TestDesktopContractVersionFlagPrintsOnlyTheBuildVersion(t *testing.T) {
	previous := version.Version
	t.Cleanup(func() {
		version.Version = previous
		rootCmd.Version = previous
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
	})
	for _, tc := range desktopVersionForms {
		t.Run(tc.name, func(t *testing.T) {
			// rootCmd.Version is read from version.Version when the package initialises; a build
			// sets the variable at link time, before that, so the two always hold the same string.
			version.Version, rootCmd.Version = tc.version, tc.version
			var out strings.Builder
			rootCmd.SetOut(&out)
			rootCmd.SetArgs([]string{"--version"})
			if err := rootCmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.version+"\n" {
				t.Errorf("`--version` printed %q, want exactly %q and a newline", out.String(), tc.version)
			}
			if got := strings.TrimSpace(captureStdout(t, func() { versionCmd.Run(versionCmd, nil) })); got != tc.version {
				t.Errorf("`runos version` printed %q, want %q", got, tc.version)
			}
			// The updater and the daemon read the same variable.
			if update.CurrentVersion() != tc.version {
				t.Errorf("update.CurrentVersion() = %q, want %q", update.CurrentVersion(), tc.version)
			}
		})
	}
}

// The CLI's own notice and Desktop's Restart VPN item must agree: no drift for the same string in
// any of the three forms, drift for any two different ones.
func TestDesktopContractDaemonVersionDriftIsAStringComparison(t *testing.T) {
	for _, a := range desktopVersionForms {
		for _, b := range desktopVersionForms {
			if got, want := vpnDaemonNeedsRestart(true, a.version, b.version), a.version != b.version; got != want {
				t.Errorf("daemon %q against CLI %q: needs restart = %t, want %t", a.version, b.version, got, want)
			}
		}
	}
}

func TestDesktopContractDesktopClassifiesEveryBuildForm(t *testing.T) {
	want := map[string]string{"release tag": "supported", "pre-release tag": "supported", "dev-stamped build": "development"}
	for _, tc := range desktopVersionForms {
		if got := desktopClassify(tc.version); got != want[tc.name] {
			t.Errorf("%s %q is classified %q, want %q", tc.name, tc.version, got, want[tc.name])
		}
	}
	for _, bad := range []string{"", "1.22", "1.22.2.x", "runos version 1.22.2", "1.22.x", "1.23.0-"} {
		if got := desktopClassify(bad); got != "invalid" {
			t.Errorf("%q is classified %q, want invalid", bad, got)
		}
	}
}
