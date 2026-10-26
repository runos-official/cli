package vpn

import (
	"encoding/json"
	"testing"
)

/*
RunOS Desktop offers "Restart VPN" when the version the daemon reports is not the version
`runos --version` prints, compared as plain strings. So the daemon must report exactly the string it
was started with (cmd/vpn_install.go passes the CLI's own version.Version), in the three forms a
build can have, and a restart, which is a new process started by the new binary, must change the
reported string to the CLI's and nothing else.
*/

var buildVersions = map[string]string{
	"release":     "1.22.2",
	"pre-release": "1.23.0-rc.1",
	"dev build":   "dev-2026-10-26T12:00:00Z",
}

func reportedVersion(t *testing.T, d *Daemon) string {
	t.Helper()
	out, err := json.Marshal(d.statusLocked())
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Version
}

func TestDaemonReportsTheVersionItWasStartedWithUnchanged(t *testing.T) {
	for name, v := range buildVersions {
		t.Run(name, func(t *testing.T) {
			d, err := NewDaemon(t.TempDir(), v, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := reportedVersion(t, d); got != v {
				t.Errorf("status reports version %q, want %q: Desktop compares the strings", got, v)
			}
		})
	}
}

// A restart is a new daemon over the same state directory, started by the updated binary. Its
// status carries the new version, and the old one leaves no trace in the saved state.
func TestRestartMakesTheDaemonReportTheCLIVersion(t *testing.T) {
	stateDir := t.TempDir()
	before, err := NewDaemon(stateDir, "1.22.2", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportedVersion(t, before); got != "1.22.2" {
		t.Fatalf("before the update the daemon reports %q", got)
	}
	for name, cli := range buildVersions {
		t.Run(name, func(t *testing.T) {
			after, err := NewDaemon(stateDir, cli, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := reportedVersion(t, after); got != cli {
				t.Errorf("after a restart the daemon reports %q, want the CLI's %q", got, cli)
			}
		})
	}
}
