package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// unreadableLog is a log the daemon wrote that this user may not read: the case a shared host
// creates on purpose now that the log is for the control-socket group.
func unreadableLog(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a unix user who is not root")
	}
	path := filepath.Join(t.TempDir(), "daemon.log")
	if err := os.WriteFile(path, []byte("vpn: step=interface status=failed no\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	old := vpnLogPathsForRead
	vpnLogPathsForRead = func() []string { return []string{path} }
	t.Cleanup(func() { vpnLogPathsForRead = old })
	return path
}

func TestLogsSaysItCannotReadTheLogInsteadOfClaimingThereIsNone(t *testing.T) {
	path := unreadableLog(t)
	cmd := &cobra.Command{Use: "logs"}
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := vpnLogsCmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("an unreadable log must fail the command")
	}
	for _, want := range []string{path, "cannot read", "sudo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "no VPN daemon log found") {
		t.Errorf("the log exists; saying it does not sends the person after the wrong problem: %v", err)
	}
}

func TestDiagnoseSaysItCannotReadTheLogInsteadOfNoneRecorded(t *testing.T) {
	isolatedHome(t)
	path := unreadableLog(t)
	cmd := &cobra.Command{Use: "diagnose", RunE: runVPNDiagnose}
	cmd.Flags().BoolP("json", "j", false, "")
	cmd.Flags().Int("lines", 40, "")
	cmd.Flags().String("socket", filepath.Join(t.TempDir(), "no.sock"), "")
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COULD NOT BE READ", path, "cannot read"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "none recorded") {
		t.Errorf("an unreadable log is not \"none recorded\":\n%s", out.String())
	}
}
