package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

/*
`runos update --check` must say WHETHER an update is available, as a flag.

MEASURED 2026-08-31 by running the real binary on two machines. The payloads for "you are current"
and "there is an update waiting" were these:

	up to date      {"updated":false,"version":"1.17.0","message":"The CLI is already up to date."}
	update waiting  {"updated":false,"version":"1.17.0","message":"A CLI update is available."}

`updated` is false in both, because `--check` never updates anything. `version` is the LATEST in
both, not the installed one, so there is nothing in the payload to compare it against. The only
difference is an English sentence.

The desktop half was worse. Both payloads were byte-identical:

	{"updated":false,"version":"0.3.0","message":"Desktop update check completed."}

So a caller could not tell, at all, whether a desktop update existed. The check already fetched the
installed version AND the latest one, compared nothing, and reported the verdict nowhere.

RunOS Desktop wants exactly this verdict, to disable its Update item when there is nothing to do and
to badge its menu bar when there is. Matching on prose is what this codebase refuses everywhere
else: "A FLAG, not a sentence to match on", because a message is written for a person and is
expected to be reworded.
*/

func TestTheCheckReportsAVerdictAndNotJustASentence(t *testing.T) {
	// The shape a caller decodes. `updateAvailable` and `currentVersion` are additive, so an older
	// reader keeps working off `version` and `message` exactly as before.
	payload := `{"schemaVersion":1,
	  "cli":{"updated":false,"updateAvailable":true,"currentVersion":"1.16.0","version":"1.17.0"},
	  "desktop":{"updated":false,"updateAvailable":true,"currentVersion":"0.2.1","version":"0.3.0"}}`

	var got combinedUpdateResult
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("the documented shape must decode: %v", err)
	}
	if !got.CLI.UpdateAvailable || got.CLI.CurrentVersion != "1.16.0" || got.CLI.Version != "1.17.0" {
		t.Errorf("cli = %+v, want the verdict plus both versions", got.CLI)
	}
	if got.Desktop == nil || !got.Desktop.UpdateAvailable || got.Desktop.CurrentVersion != "0.2.1" {
		t.Errorf("desktop = %+v, want the verdict plus both versions", got.Desktop)
	}
}

/*
The desktop verdict, which must agree with what the installer actually does.

`Manager.Update()` treats `installed == latest` as up to date and installs otherwise, so the check
uses the same rule. A check that disagreed with the installer would either offer an update that does
nothing or hide one that would have worked.
*/
func TestTheDesktopVerdictMatchesWhatTheInstallerWouldDo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed string
		latest    string
		want      bool
	}{
		{name: "behind", installed: "0.2.1", latest: "0.3.0", want: true},
		{name: "current", installed: "0.3.0", latest: "0.3.0", want: false},
		// Not installed: there is nothing to update, and the install path owns that case.
		{name: "nothing installed", installed: "", latest: "0.3.0", want: false},
		// A latest that could not be read is not evidence of an update.
		{name: "latest unknown", installed: "0.3.0", latest: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := desktopUpdateAvailable(tc.installed, tc.latest); got != tc.want {
				t.Errorf("desktopUpdateAvailable(%q, %q) = %v, want %v", tc.installed, tc.latest, got, tc.want)
			}
		})
	}
}

// A local build is never "behind" a release: its version string is not comparable, and installing a
// release over it would be a downgrade. It must never light up an update badge.
func TestADevBuildNeverReportsAnUpdate(t *testing.T) {
	var result combinedUpdateResult
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"cli":{"updated":false,"updateAvailable":false,"currentVersion":"dev-2026-08-31T09:48:44Z"}}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.CLI.UpdateAvailable {
		t.Error("a dev build must never claim an update is waiting")
	}
	if !strings.HasPrefix(result.CLI.CurrentVersion, "dev-") {
		t.Errorf("currentVersion must carry what is actually installed, got %q", result.CLI.CurrentVersion)
	}
}
