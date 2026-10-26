package cmd

import (
	"strings"
	"testing"

	"github.com/runos-official/cli/internal/desktopcontract"
	"github.com/runos-official/cli/internal/vpn"

	"github.com/spf13/cobra"
)

// RunOS Desktop reads the JSON these commands print with strict types. internal/desktopcontract
// holds the documented table; every case here runs real command output through it. See
// docs/desktop-contract.md.

// vpnJSON runs one vpn command's handler with --json against a fake daemon and returns stdout.
func vpnJSON(t *testing.T, run func(*cobra.Command, []string) error, socket string, args ...string) string {
	t.Helper()
	var out strings.Builder
	cmd := &cobra.Command{Use: "x", RunE: run}
	cmd.Flags().BoolP("json", "j", false, "")
	cmd.Flags().String("socket", "", "")
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(append([]string{"--json", "--socket", socket}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	return out.String()
}

// A daemon that answers without a status must not make `--json` print the word null: Desktop reads
// the output as an object and loses the menu when it is not one.
func TestVPNStatusJSONIsAnObjectWhateverTheDaemonAnswers(t *testing.T) {
	populated := &vpn.Status{}
	desktopcontract.Populate(populated)
	cases := map[string]*vpn.Status{"no status": nil, "zero status": {}, "populated status": populated}
	for name, status := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "x"}
			cmd.Flags().BoolP("json", "j", true, "")
			var out strings.Builder
			cmd.SetOut(&out)
			if err := emitVPNStatus(cmd, status); err != nil {
				t.Fatal(err)
			}
			desktopcontract.Check(t, name, []byte(out.String()), desktopcontract.VPNStatus)
		})
	}
}

// The commands Desktop runs that print the daemon's status, end to end through the socket: the
// daemon encodes, the CLI decodes and encodes again, and the result must still conform.
func TestEveryVPNCommandPrintsAContractStatus(t *testing.T) {
	daemon := newFakeDaemon(t)
	cases := map[string]struct {
		run  func(*cobra.Command, []string) error
		args []string
	}{
		"vpn status":     {runVPNStatus, nil},
		"vpn down":       {runVPNDown, nil},
		"vpn connect":    {runVPNConnect, []string{"c1234"}},
		"vpn disconnect": {runVPNDisconnect, []string{"c1234"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := vpnJSON(t, tc.run, daemon.path, tc.args...)
			desktopcontract.Check(t, name, []byte(out), desktopcontract.VPNStatus)
		})
	}
}
