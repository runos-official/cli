package desktop

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

/*
`Update` must never replace a bundle with an older one.

WHAT WAS UNTESTED. This guard was the headline of 0da36c4, and the two tests that shipped with it
exercise `desktopUpdateAvailable` in package cmd, which is the `--check` VERDICT, not the
installer. Nothing called `Update` at all, so the comparison could be reverted to equality, or its
arguments flipped, and the whole suite stayed green. Two independent verifiers did exactly that.

WHY IT MATTERS THAT NOTHING ELSE COVERS IT. `Install` never compares versions, and both callers
reach `Update` directly: `runos update` calls it on the non-check path as soon as a bundle is
installed, and `runos desktop update` calls it bare. This line is the only thing between a locally
built 0.5.0 and a 0.4.0 release.

FLIPPING THE ARGUMENTS is the subtler half, and it is covered here too: `!IsNewerVersion(installed,
latest)` instead of `!IsNewerVersion(latest, installed)` would make every up-to-date machine
reinstall on every run, and the equal-versions case below is what catches it.
*/
func TestUpdateNeverInstallsAnOlderReleaseOverANewerBundle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed string
		latest    string
		wantSkip  bool
	}{
		// THE DEFECT: a local build ahead of the latest release. Equality would replace it.
		{name: "installed is ahead of the release", installed: "0.5.0", latest: "0.4.0", wantSkip: true},
		// Flipping the comparison's arguments would reinstall here, on every machine, every run.
		{name: "installed matches the release", installed: "0.4.0", latest: "0.4.0", wantSkip: true},
		// And the ordinary case must still install, or the guard has eaten the feature.
		{name: "installed is behind the release", installed: "0.3.0", latest: "0.4.0", wantSkip: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeFakeBundle(t, home, tc.installed)
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/releases/latest" {
					fmt.Fprintf(w, `{"tag_name":"v%s"}`, tc.latest)
					return
				}
				// Any other path means it went to fetch the release, which is the thing under test.
				downloads++
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()
			manager := &Manager{
				HTTPClient:     server.Client(),
				HomeDir:        home,
				ReleasesAPIURL: server.URL + "/releases/latest",
				ReleaseBaseURL: server.URL + "/download",
			}

			result, err := manager.Update()

			if tc.wantSkip {
				if err != nil {
					t.Fatalf("an up-to-date bundle is not an error: %v", err)
				}
				if result.Updated {
					t.Errorf("reported an update it must not have performed: %+v", result)
				}
				if downloads != 0 {
					t.Errorf("fetched a release it must not have installed (%d requests)", downloads)
				}
				return
			}
			// Behind the release: it must actually TRY. The fake serves no archive, so the attempt
			// fails, and that failure is the evidence the guard let it through.
			if err == nil && downloads == 0 {
				t.Error("a bundle behind the release must be updated, and nothing was fetched")
			}
		})
	}
}

// writeFakeBundle puts just enough of an application bundle on disk for Status to read a version
// out of it: the Info.plist is the only part `applicationVersion` looks at.
func writeFakeBundle(t *testing.T, home, version string) {
	t.Helper()
	contents := filepath.Join(home, "Applications", applicationName, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleShortVersionString</key><string>%s</string>
</dict></plist>
`, version)
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}
